package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AddParkedPath records a parked root. Paths are stored exactly as given;
// callers normalise them first. It returns ErrExists if already parked.
func (s *Store) AddParkedPath(ctx context.Context, path string) error {
	if path == "" {
		return errors.New("park path: path is empty")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO parked_paths (path, added_at) VALUES (?, ?)`,
		path, formatTime(time.Now()))
	if isUniqueViolation(err) {
		return fmt.Errorf("park %s: %w", path, ErrExists)
	}
	if err != nil {
		return fmt.Errorf("park %s: %w", path, err)
	}
	return nil
}

// RemoveParkedPath forgets a parked root, or returns ErrNotFound.
func (s *Store) RemoveParkedPath(ctx context.Context, path string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM parked_paths WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("forget %s: %w", path, err)
	}
	return requireOne(res, "forget "+path)
}

// ParkedPaths returns every parked root in the order they were added, which
// is the precedence order for name collisions between roots.
func (s *Store) ParkedPaths(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM parked_paths ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list parked paths: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("list parked paths: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list parked paths: %w", err)
	}
	return out, nil
}
