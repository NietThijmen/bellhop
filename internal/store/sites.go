package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Site sources.
const (
	SourceLinked = "linked"
	SourceParked = "parked"
)

// Site is a linked site, or a parked site with custom settings. Plain parked
// sites are discovered from disk and have no row.
type Site struct {
	ID              int64
	Name            string
	Path            string
	Source          string // SourceLinked or SourceParked
	PHPVersion      string // "" = default
	Secure          bool
	Driver          string // "" = auto-detect
	DocrootOverride string
	Aliases         []string
}

const siteColumns = `id, name, path, source, php_version, secure, driver, docroot_override, aliases_json`

// UpsertSite creates the site or replaces every field of the existing site
// with the same Name. Site.ID is ignored; read it back with GetSite.
func (s *Store) UpsertSite(ctx context.Context, site Site) error {
	if site.Name == "" {
		return errors.New("save site: name is empty")
	}
	if site.Source != SourceLinked && site.Source != SourceParked {
		return fmt.Errorf("save site %s: source %q must be %q or %q", site.Name, site.Source, SourceLinked, SourceParked)
	}
	aliases := site.Aliases
	if aliases == nil {
		aliases = []string{}
	}
	aj, err := json.Marshal(aliases)
	if err != nil {
		return fmt.Errorf("save site %s: encode aliases: %w", site.Name, err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sites (name, path, source, php_version, secure, driver, docroot_override, aliases_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (name) DO UPDATE SET
		   path = excluded.path, source = excluded.source, php_version = excluded.php_version,
		   secure = excluded.secure, driver = excluded.driver,
		   docroot_override = excluded.docroot_override, aliases_json = excluded.aliases_json`,
		site.Name, site.Path, site.Source, site.PHPVersion, boolInt(site.Secure),
		site.Driver, site.DocrootOverride, string(aj))
	if err != nil {
		return fmt.Errorf("save site %s: %w", site.Name, err)
	}
	return nil
}

// GetSite returns the site with the given name, or ErrNotFound.
func (s *Store) GetSite(ctx context.Context, name string) (Site, error) {
	site, err := scanSite(s.db.QueryRowContext(ctx, `SELECT `+siteColumns+` FROM sites WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, fmt.Errorf("site %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Site{}, fmt.Errorf("read site %s: %w", name, err)
	}
	return site, nil
}

// DeleteSite removes the site row with the given name, or returns ErrNotFound.
func (s *Store) DeleteSite(ctx context.Context, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sites WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete site %s: %w", name, err)
	}
	return requireOne(res, "delete site "+name)
}

// ListSites returns every site row ordered by name.
func (s *Store) ListSites(ctx context.Context) ([]Site, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+siteColumns+` FROM sites ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Site
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, fmt.Errorf("list sites: %w", err)
		}
		out = append(out, site)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	return out, nil
}

func scanSite(r rowScanner) (Site, error) {
	var (
		site    Site
		secure  int
		aliases string
	)
	if err := r.Scan(&site.ID, &site.Name, &site.Path, &site.Source, &site.PHPVersion, &secure,
		&site.Driver, &site.DocrootOverride, &aliases); err != nil {
		return Site{}, err
	}
	site.Secure = secure != 0
	if err := json.Unmarshal([]byte(aliases), &site.Aliases); err != nil {
		return Site{}, fmt.Errorf("decode aliases of site %s: %w", site.Name, err)
	}
	if len(site.Aliases) == 0 {
		site.Aliases = nil
	}
	return site, nil
}
