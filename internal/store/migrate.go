package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nietthijmen/bellhop/pkg/version"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

// migrationsFS is the migrations directory as a flat FS of NNNN_name.sql files.
var migrationsFS = mustSub(embeddedMigrations, "migrations")

func mustSub(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic(err) // embed pattern guarantees the directory exists
	}
	return sub
}

type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads NNNN_description.sql files from fsys, sorted by
// version. Duplicate or malformed version numbers are programming errors.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %s: name must be NNNN_description.sql", e.Name())
		}
		v, err := strconv.Atoi(prefix)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("migration %s: name must start with a positive number", e.Name())
		}
		if prev, dup := seen[v]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", prev, e.Name(), v)
		}
		seen[v] = e.Name()
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// migrate applies every migration in fsys newer than the database's highest
// recorded version, each in its own transaction, and records it in
// schema_migrations. It is forward-only: a database that already records a
// version this binary does not know was written by a newer build and is
// refused rather than touched.
func migrate(ctx context.Context, db *sql.DB, fsys fs.FS) error {
	ms, err := loadMigrations(fsys)
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	var current sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	latest := 0
	if len(ms) > 0 {
		latest = ms[len(ms)-1].version
	}
	if current.Valid && int(current.Int64) > latest {
		return fmt.Errorf("database schema version %d is newer than this build supports (%d); upgrade %s to the version that created it, or restore an older backup of the database",
			current.Int64, latest, version.Name)
	}

	for _, m := range ms {
		if current.Valid && m.version <= int(current.Int64) {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
		slog.Info("applied database migration", "version", m.version, "name", m.name)
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, m migration) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("apply migration %s: begin transaction: %w", m.name, err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, rollback(tx))
		}
	}()
	// Another process may have applied it between our version check and
	// taking the write lock (transactions are BEGIN IMMEDIATE).
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&exists); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	if exists > 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("apply migration %s: %w; the database was left unchanged, report this as a bug", m.name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, formatTime(time.Now())); err != nil {
		return fmt.Errorf("record migration %s: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.name, err)
	}
	return nil
}

func rollback(tx *sql.Tx) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("rollback: %w", err)
	}
	return nil
}

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return int(v.Int64), nil
}
