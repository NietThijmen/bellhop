// Package store is the daemon's persistent state: a SQLite database (pure Go,
// via modernc.org/sqlite) with embedded forward-only schema migrations and
// small typed repositories for settings, parked paths, sites, PHP versions,
// services and jobs.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite" // also registers the "sqlite" database/sql driver
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/nietthijmen/bellhop/pkg/version"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when inserting a row that violates a uniqueness rule
// (for example parking the same path twice).
var ErrExists = errors.New("already exists")

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// busyTimeoutMS is how long a connection waits on a locked database before
// failing with SQLITE_BUSY.
const busyTimeoutMS = 5000

// timeLayout is a fixed-width RFC 3339 layout with nanoseconds, always in UTC.
// It parses with time.RFC3339Nano, and unlike RFC3339Nano it never trims
// trailing zeros, so stored timestamps sort correctly as text.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// Store is a handle to the state database. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the database at path, configures WAL mode,
// a 5 s busy timeout and foreign keys on every connection, and applies any
// pending embedded migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("open state database: empty path; pass the database file location (see platform.Dirs.DBPath)")
	}
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("open state database %q: path must not contain '?' or '#'; move %s's data directory to a path without them", path, version.Name)
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("open state database: create directory %s: %w; check that it is writable", dir, err)
		}
	}

	db, err := sql.Open(driverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open state database %s: %w", path, err)
	}
	// SQLite allows one writer at a time. _txlock=immediate makes every
	// transaction take the write lock up front, so concurrent writers queue on
	// busy_timeout instead of failing on lock upgrade. Readers proceed in
	// parallel under WAL.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	db.SetConnMaxIdleTime(5 * time.Minute)

	s := &Store{db: db, path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open state database %s: %w; check that the file is a %s database and is not locked by another program", path, err, version.Name)
	}
	if err := migrate(ctx, db, migrationsFS); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open state database %s: %w", path, err)
	}
	return s, nil
}

// dsn builds the modernc.org/sqlite DSN. The driver splits the path from the
// query at the first '?', and runs each _pragma as "PRAGMA <value>" on every
// new connection.
func dsn(path string) string {
	return path + "?" + strings.Join([]string{
		fmt.Sprintf("_pragma=busy_timeout(%d)", busyTimeoutMS),
		"_pragma=foreign_keys(1)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_txlock=immediate",
	}, "&")
}

// Close closes the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close state database %s: %w", s.path, err)
	}
	return nil
}

// DB exposes the underlying handle for callers that need ad-hoc queries.
func (s *Store) DB() *sql.DB { return s.db }

// Path is the database file location.
func (s *Store) Path() string { return s.path }

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp %q: %w", v, err)
	}
	return t.UTC(), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation reports whether err is a SQLite UNIQUE or PRIMARY KEY
// constraint failure.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

// requireOne turns a zero-row write into ErrNotFound.
func requireOne(res sql.Result, what string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, ErrNotFound)
	}
	return nil
}
