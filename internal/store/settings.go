package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// GetSetting returns the value for key and whether it was set.
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read setting %q: %w", key, err)
	}
	return v, true, nil
}

// SetSetting creates or replaces a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("save setting: key is empty")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("save setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a setting. Deleting a missing key is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("delete setting %q: %w", key, err)
	}
	return nil
}

// Settings returns every stored setting.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("list settings: %w", err)
		}
		out[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	return out, nil
}
