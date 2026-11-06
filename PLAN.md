# Bellhop implementation plan

Status of the v1 implementation plan ("Bellhop — Cross-platform Valet in Go:
Implementation Plan", 2026-11-05) and the work that remains. That document is
the spec; this file tracks progress against it. Deviations from the spec are
recorded as ADRs in [`docs/decisions/`](docs/decisions/).

Last updated: 2026-11-06.

## Status by milestone

| Milestone | Status | Notes |
|---|---|---|
| M0 Repository and CI | Done | Taskfile + Makefile ([ADR 0001](docs/decisions/0001-task-runner-and-toolchain.md)), golangci-lint, CI matrix: lint per GOOS, race + integration tests on macOS/Ubuntu/Windows, cgo-free cross-builds. |
| M1 Daemon core | Done | `store`, `ipc` (Unix socket / named pipe), `/v1/status`, SSE `/v1/events`, `supervisor`, `artifacts`, `jobs`, CLI skeleton with `--json`. |
| M2 Serving sites | Done | All drivers with fixture tests, embedded Caddy, park/link/unlink/sites/open, fsnotify watching of parked roots. |
| M3 Windows PHP parity | Done | `php-cgi` pool, recycling via `PHP_FCGI_MAX_REQUESTS`, least-conn balancing, recycle test. |
| M4 DNS, HTTPS, setup | Done | DNS resolver, local CA, `bellhop-helper` with undo, `setup`/`uninstall`/`tld`/`doctor`, secure/unsecure. |
| M5 PHP versions | Done | install/remove/use/isolate/unisolate, lazy pools, `php`/`composer` shims, `99-user.ini` kept across reinstall. |
| M6 MariaDB | Done | install/init/supervise, port fallback (3307+), provision from `.env`/`wp-config.php`, import/export, migrate, `mariadb-upgrade`. macOS uses Homebrew MariaDB until decision 1 is resolved. |
| M7 Mail | Not started | |
| M8 Wails GUI | Not started | |
| M9 Packaging and hardening | Not started | |

**Manual acceptance checks still owed for M2–M6.** These cannot be covered by
unit tests and have not been recorded as done: trusted HTTPS in
Chrome/Firefox/Safari/Edge and `curl` without `-k`; setcap binding on Linux;
fresh Laravel and WordPress installs (admin, pretty permalinks, multisite);
two PHP versions serving concurrently; 100 MB `.sql.gz` import/export
round-trip; `uninstall` leaving nothing behind on each OS.

## Remaining work

One focused PR per item, with tests, conventional commits. Items within a
phase are in dependency order.

### Phase A — cleanup (small)

1. **API path deviations.** The database routes are `/v1/db` and
   `/v1/db/{name}` instead of the spec's `/v1/db/databases[/{name}]`. Either
   rename them (CLI and client updated together) or record an ADR. Same check
   for any other route that differs from the spec table.
2. **Stale TODO** in `internal/daemon/registry.go` (`TODO(M2+)`): the db and
   dns providers exist; update the comment to list what is still missing
   (mail) and whether `Autostart` comes from `store.ServiceConfig.Enabled`.
3. **Per-site logs.** `bellhop logs` only accepts services; the spec says
   `bellhop logs [service|site] [-f]`. Add a site log endpoint (access log
   under `<logs>/sites/<name>.log`) and accept a site name in the CLI. The GUI
   site detail screen needs the same endpoint.

### Phase B — M7 Mail (Mailpit)

4. **Manifest.** Pin a Mailpit release in `manifests/artifacts.json` for
   darwin/linux/windows × amd64/arm64 with SHA-256 sums from the release
   assets. Record the pinned version; all endpoints below come from that
   version's OpenAPI spec, not from memory.
5. **`internal/mail` service.** Install via `artifacts`; supervisor spec
   `mailpit --smtp 127.0.0.1:<smtp> --listen 127.0.0.1:<http> --database
   <data>/mail/mailpit.db --max <n>`; HTTP readiness check; port conflict
   detection for 1025/8025 with fallback and a `daemon.warning`. Register a
   `mailProvider` in the daemon. Settings: SMTP port, HTTP port, max messages.
6. **PHP wiring.** In each version's `conf.d/10-bellhop.ini`: on macOS/Linux
   `sendmail_path = "<mailpit> sendmail -S 127.0.0.1:<smtp>"`; on Windows
   `SMTP`, `smtp_port`, `sendmail_from = bellhop@localhost`. Regenerate the
   ini files and restart running pools when mail ports change. Never touch
   `99-user.ini`.
7. **Proxy route.** `mail.<tld>` → reverse proxy to the Mailpit web UI,
   including its websocket. Secure like any other site.
8. **API client + events.** Typed client for list/search/get (HTML, text,
   headers, raw, attachments)/delete/mark-read; websocket subscriber that
   emits `mail.new` within 1 s of delivery and reconnects with backoff.
9. **Daemon routes.** `GET /v1/mail/messages`, `GET /v1/mail/messages/{id}`,
   `DELETE /v1/mail/messages[/{id}]`, `POST /v1/sites/{name}/mail/configure-env`.
   The configure call has a dry-run mode returning the `.env` diff; writing
   requires a second, confirmed call (uses `envfile`, writes the backup).
10. **CLI.** `bellhop mail open|list|clear|configure`; `configure` prints the
    diff and asks before writing.
11. **Tests.** Unit tests for ini generation and the client (against a fake
    server). Integration test (tag `integration`): PHP `mail()` reaches
    Mailpit and `mail.new` fires. Manual: WordPress `wp_mail()` and Laravel
    `Mail::raw()` on every OS.

### Phase C — M8 Wails GUI

12. **Decisions first (ADRs).**
    - Decision 3: check whether Wails v3 has a stable release; use v3 for
      native tray support if so, otherwise v2 and move the tray to v1.1.
    - Give `cmd/bellhop-app` its own `go.mod` so Wails and cgo stay out of the
      main module, keeping `internal/` cgo-free by construction.
13. **API gaps the GUI needs** (daemon + `internal/api` + client):
    provisioning dry-run that returns what was read from `.env` /
    `wp-config.php`; settings for ports, Mailpit limits and MariaDB root
    password; update-check endpoint (see item 20); per-site logs (item 3).
14. **App shell.** `cmd/bellhop-app/app.go` with an `App` whose methods map
    1:1 to IPC client calls; one goroutine relaying `/v1/events` to Wails
    events with reconnect/backoff; "daemon not running" screen that starts it
    via the platform autostart mechanism.
15. **Frontend scaffold.** Svelte 5 (runes) + TypeScript + Vite + Tailwind +
    Bits UI; wrappers in `frontend/src/lib/components/ui`; rune stores in
    `.svelte.ts` fed by events, 30 s status refresh as safety net; small
    client router (or SvelteKit with adapter-static only); light/dark from OS.
16. **Screens**, in this order: Dashboard → Sites → Site detail → PHP →
    Database → Mail (sandboxed `srcdoc` iframe without `allow-scripts`, remote
    content blocked by default) → Logs → Settings → Onboarding (last, because
    it drives everything else).
17. **Tray and notifications** (Wails v3 only): health icon, recent sites,
    start/stop all, Mailpit, unread count; notifications for new mail and
    service failures; hidden autostart at login.
18. **Tests and CI.** Vitest + @testing-library/svelte; `eslint` and
    `svelte-check` in `ci.yml`; per-release manual smoke checklist in `docs/`.

### Phase D — M9 Packaging and hardening

19. **`doctor` coverage.** Add conflict checks for 3306/1025/8025 with owning
    process, `/etc/resolver/<tld>` not written by Bellhop, Valet present
    (decision 6), missing VC++ runtime, Linux MariaDB runtime libs.
20. **Update check.** GitHub Releases, notify only; daemon endpoint used by
    CLI (`bellhop status`/`doctor`) and GUI settings.
21. **Release workflow** (`release.yml` on tags): native Wails builds per OS;
    signed + notarised macOS `.app`/`.dmg` (decision 5); NSIS per-user
    installer bootstrapping WebView2 whose uninstaller runs
    `bellhop uninstall`; `.deb`/`.rpm` via nfpm declaring WebKitGTK plus a
    tarball; checksums; draft GitHub Release. Re-apply setcap after updates.
22. **PHP builds.** `php-builds.yml` (may live in a separate repo):
    static-php-cli builds with the pinned extension list for macOS
    arm64/x86_64 and Linux x86_64/aarch64, published with SHA-256; manifest
    updated by PR.
23. **Schema migration tests.** Keep a DB fixture from every release and test
    forward migration from each.
24. **User docs** in `docs/`: install, CLI reference (generated from cobra),
    troubleshooting, uninstall.

## Open decisions

From the spec, with the current state:

| # | Decision | State |
|---|---|---|
| 1 | MariaDB on macOS | Default in place: Homebrew `mariadbd` with Bellhop's datadir (`TODO(decision-1)`). Own macOS builds still to do. |
| 2 | Xdebug with static PHP | Not started; v1 ships without it on macOS/Linux. |
| 3 | Wails major version | Decide at the start of phase C (item 12). |
| 4 | Bundle id / repo | `io.github.nietthijmen.bellhop`, module `github.com/nietthijmen/bellhop`. |
| 5 | Code signing | Needed before the first macOS release. |
| 6 | Coexistence with Valet | Setup detects Valet; covered again by `doctor` in item 19. |
| 7 | Licence | Spec default is MIT; the licence file was removed in `036efc1`. Needs a decision before the first release. |
| 8 | MariaDB version line | Pinned to 12.3.3 in the manifest. |
