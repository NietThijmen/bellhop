package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PHPVersion is an installed PHP build.
type PHPVersion struct {
	Version     string // e.g. "8.3"
	Path        string // install directory
	InstalledAt time.Time
}

// AddPHPVersion records an installed PHP version, replacing the row for the
// same version (a reinstall). A zero InstalledAt is stored as now.
func (s *Store) AddPHPVersion(ctx context.Context, v PHPVersion) error {
	if v.Version == "" {
		return errors.New("save PHP version: version is empty")
	}
	at := v.InstalledAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO php_versions (version, path, installed_at) VALUES (?, ?, ?)
		 ON CONFLICT (version) DO UPDATE SET path = excluded.path, installed_at = excluded.installed_at`,
		v.Version, v.Path, formatTime(at))
	if err != nil {
		return fmt.Errorf("save PHP %s: %w", v.Version, err)
	}
	return nil
}

// RemovePHPVersion forgets an installed PHP version, or returns ErrNotFound.
func (s *Store) RemovePHPVersion(ctx context.Context, version string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM php_versions WHERE version = ?`, version)
	if err != nil {
		return fmt.Errorf("remove PHP %s: %w", version, err)
	}
	return requireOne(res, "remove PHP "+version)
}

// GetPHPVersion returns one installed PHP version, or ErrNotFound.
func (s *Store) GetPHPVersion(ctx context.Context, version string) (PHPVersion, error) {
	v, err := scanPHP(s.db.QueryRowContext(ctx,
		`SELECT version, path, installed_at FROM php_versions WHERE version = ?`, version))
	if errors.Is(err, sql.ErrNoRows) {
		return PHPVersion{}, fmt.Errorf("PHP %s: %w", version, ErrNotFound)
	}
	if err != nil {
		return PHPVersion{}, fmt.Errorf("read PHP %s: %w", version, err)
	}
	return v, nil
}

// PHPVersions returns every installed PHP version ordered by version string.
func (s *Store) PHPVersions(ctx context.Context) ([]PHPVersion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version, path, installed_at FROM php_versions ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("list PHP versions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []PHPVersion
	for rows.Next() {
		v, err := scanPHP(rows)
		if err != nil {
			return nil, fmt.Errorf("list PHP versions: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list PHP versions: %w", err)
	}
	return out, nil
}

func scanPHP(r rowScanner) (PHPVersion, error) {
	var (
		v  PHPVersion
		at string
	)
	if err := r.Scan(&v.Version, &v.Path, &at); err != nil {
		return PHPVersion{}, err
	}
	t, err := parseTime(at)
	if err != nil {
		return PHPVersion{}, err
	}
	v.InstalledAt = t
	return v, nil
}
