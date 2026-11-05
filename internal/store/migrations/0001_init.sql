-- 0001_init: initial schema. Migrations are forward-only: never edit a file
-- that has shipped; add a new numbered file instead.

CREATE TABLE settings (
    key   TEXT PRIMARY KEY NOT NULL,
    value TEXT NOT NULL
);

-- Parked roots. id order is the order roots were added, which decides
-- name collisions between two parked roots (first added wins).
CREATE TABLE parked_paths (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    path     TEXT NOT NULL UNIQUE,
    added_at TEXT NOT NULL
);

-- Linked sites, and parked sites that carry custom settings.
CREATE TABLE sites (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    name             TEXT NOT NULL UNIQUE,
    path             TEXT NOT NULL,
    source           TEXT NOT NULL CHECK (source IN ('linked', 'parked')),
    php_version      TEXT NOT NULL DEFAULT '',
    secure           INTEGER NOT NULL DEFAULT 0 CHECK (secure IN (0, 1)),
    driver           TEXT NOT NULL DEFAULT '',
    docroot_override TEXT NOT NULL DEFAULT '',
    aliases_json     TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE php_versions (
    version      TEXT PRIMARY KEY NOT NULL,
    path         TEXT NOT NULL,
    installed_at TEXT NOT NULL
);

CREATE TABLE services (
    name    TEXT PRIMARY KEY NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    port    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE jobs (
    id          TEXT PRIMARY KEY NOT NULL,
    kind        TEXT NOT NULL,
    status      TEXT NOT NULL,
    progress    REAL NOT NULL DEFAULT 0,
    message     TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    result      BLOB,
    started_at  TEXT NOT NULL,
    finished_at TEXT,
    log_path    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX jobs_started_at ON jobs (started_at);
CREATE INDEX jobs_status ON jobs (status);
