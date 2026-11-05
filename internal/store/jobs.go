package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
)

// Job is the persisted form of a long-running operation. Status holds an
// api.JobStatus value.
type Job struct {
	ID         string
	Kind       string
	Status     string
	Progress   float64
	Message    string
	Error      string
	Result     []byte
	StartedAt  time.Time
	FinishedAt *time.Time
	LogPath    string
}

const jobColumns = `id, kind, status, progress, message, error, result, started_at, finished_at, log_path`

func finishedArg(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

// InsertJob stores a new job. It returns ErrExists if the id is taken.
func (s *Store) InsertJob(ctx context.Context, j Job) error {
	if j.ID == "" {
		return errors.New("save job: id is empty")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO jobs (`+jobColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Kind, j.Status, j.Progress, j.Message, j.Error, j.Result,
		formatTime(j.StartedAt), finishedArg(j.FinishedAt), j.LogPath)
	if isUniqueViolation(err) {
		return fmt.Errorf("save job %s: %w", j.ID, ErrExists)
	}
	if err != nil {
		return fmt.Errorf("save job %s: %w", j.ID, err)
	}
	return nil
}

// UpdateJob replaces every mutable field of an existing job. It returns
// ErrNotFound if no job has j.ID.
func (s *Store) UpdateJob(ctx context.Context, j Job) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET kind = ?, status = ?, progress = ?, message = ?, error = ?, result = ?,
		 started_at = ?, finished_at = ?, log_path = ? WHERE id = ?`,
		j.Kind, j.Status, j.Progress, j.Message, j.Error, j.Result,
		formatTime(j.StartedAt), finishedArg(j.FinishedAt), j.LogPath, j.ID)
	if err != nil {
		return fmt.Errorf("update job %s: %w", j.ID, err)
	}
	return requireOne(res, "update job "+j.ID)
}

// GetJob returns one job, or ErrNotFound.
func (s *Store) GetJob(ctx context.Context, id string) (Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, fmt.Errorf("job %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Job{}, fmt.Errorf("read job %s: %w", id, err)
	}
	return j, nil
}

// ListJobs returns up to limit jobs, newest StartedAt first. A limit <= 0
// returns all jobs.
func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = -1 // SQLite: no limit
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobColumns+` FROM jobs ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("list jobs: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return out, nil
}

// FailInterruptedJobs marks every queued or running job as failed with the
// given reason and the current time as FinishedAt. The daemon calls it on
// start, since jobs never survive a restart. It returns how many were changed.
func (s *Store) FailInterruptedJobs(ctx context.Context, reason string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status = ?, error = ?, finished_at = ? WHERE status IN (?, ?)`,
		string(api.JobFailed), reason, formatTime(time.Now()),
		string(api.JobQueued), string(api.JobRunning))
	if err != nil {
		return 0, fmt.Errorf("mark interrupted jobs as failed: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark interrupted jobs as failed: %w", err)
	}
	return n, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(r rowScanner) (Job, error) {
	var (
		j        Job
		started  string
		finished sql.NullString
	)
	if err := r.Scan(&j.ID, &j.Kind, &j.Status, &j.Progress, &j.Message, &j.Error, &j.Result,
		&started, &finished, &j.LogPath); err != nil {
		return Job{}, err
	}
	t, err := parseTime(started)
	if err != nil {
		return Job{}, err
	}
	j.StartedAt = t
	if finished.Valid {
		f, err := parseTime(finished.String)
		if err != nil {
			return Job{}, err
		}
		j.FinishedAt = &f
	}
	if len(j.Result) == 0 {
		j.Result = nil
	}
	return j, nil
}
