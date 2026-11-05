# 1. Taskfile as the task runner; Go 1.27

Date: 2026-11-05 · Status: accepted

Deviations from the implementation plan are recorded in this directory as
numbered ADRs (context, decision, consequences).

## Context
The plan allows `Makefile`/`Taskfile`. Windows developers have no `make` by default.

## Decision
`Taskfile.yml` (go-task) is canonical; a thin `Makefile` mirrors it for Unix.
CI calls `go` directly so it needs neither. The module pins `go 1.27.0` with
`toolchain go1.27.3` (latest stable, released 2026-10-19); older local Go
installs download it automatically via `GOTOOLCHAIN=auto`.

## Consequences
`go test -race` needs cgo, so the CI race job sets `CGO_ENABLED=1`; every
build job keeps `CGO_ENABLED=0` to enforce the cgo-free rule for `internal/`.
