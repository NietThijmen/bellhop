package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ServiceConfig is the persisted configuration of a supervised service.
type ServiceConfig struct {
	Name    string
	Enabled bool
	Port    int
}

// GetService returns one service's configuration, or ErrNotFound.
func (s *Store) GetService(ctx context.Context, name string) (ServiceConfig, error) {
	var (
		c       ServiceConfig
		enabled int
	)
	err := s.db.QueryRowContext(ctx, `SELECT name, enabled, port FROM services WHERE name = ?`, name).
		Scan(&c.Name, &enabled, &c.Port)
	if errors.Is(err, sql.ErrNoRows) {
		return ServiceConfig{}, fmt.Errorf("service %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return ServiceConfig{}, fmt.Errorf("read service %s: %w", name, err)
	}
	c.Enabled = enabled != 0
	return c, nil
}

// UpsertService creates or replaces a service's configuration.
func (s *Store) UpsertService(ctx context.Context, c ServiceConfig) error {
	if c.Name == "" {
		return errors.New("save service: name is empty")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO services (name, enabled, port) VALUES (?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET enabled = excluded.enabled, port = excluded.port`,
		c.Name, boolInt(c.Enabled), c.Port)
	if err != nil {
		return fmt.Errorf("save service %s: %w", c.Name, err)
	}
	return nil
}

// ListServices returns every stored service configuration ordered by name.
func (s *Store) ListServices(ctx context.Context) ([]ServiceConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, enabled, port FROM services ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ServiceConfig
	for rows.Next() {
		var (
			c       ServiceConfig
			enabled int
		)
		if err := rows.Scan(&c.Name, &enabled, &c.Port); err != nil {
			return nil, fmt.Errorf("list services: %w", err)
		}
		c.Enabled = enabled != 0
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	return out, nil
}
