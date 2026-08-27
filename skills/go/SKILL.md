---
name: go
description: Go expertise for this project — concurrency model, CLI flag design, platform build tags, and idiomatic patterns specific to media_organizer.
skills:
  - fullstack-dev-skills:golang-pro
  - fullstack-dev-skills:cli-developer
---

# Go

## When to Invoke

| Skill | Invoke When |
|---|---|
| `/golang-pro` | Idiomatic Go patterns, goroutine safety, interface design, stdlib deep-dives |
| `/cli-developer` | Flag layout, UX decisions, subcommand structure, shell completion |

## Project Context

**Concurrency model** (`src/cmd/app.go`, `process.go`)
- Worker pool via buffered `chan string` + `sync.WaitGroup`
- Directory cache: `sync.Map` (goroutine-safe, no mutex needed)
- Transfer counters: `atomic.Int64`
- Copy buffer pool: `sync.Pool` of 1 MiB slices

**Interface pattern** (`app.go:ExifService`)
- Defined at the consumer site, not in `src/internal`
- Allows `App` to be tested with a fake without touching the real exiftool subprocess

**Platform split** (`fileutil_darwin.go` / `fileutil_other.go`)
- Both a `_darwin` filename suffix and a `//go:build darwin` tag are required
- Darwin adds birth-time restoration via `setattrlist(2)`; other platforms use `os.Chtimes`

**CLI flags** (`config.go`)
- Single-hyphen: `-dry-run`, `-copy`, `-workers N`, `-estimate`, `-failure-log`
- SSH key: `-ssh-key ~/.ssh/id_ed25519` — `~` is expanded via `expandTilde`
- New flags need a test in `sort_by_date_test.go`

## Key Files

- `src/cmd/app.go` — `App`, `Stats`, `ExifService` interface
- `src/cmd/config.go` — `Config`, flag parsing, SSH path normalization
- `src/cmd/process.go` — worker loop, `processFile`
- `src/internal/exiftool_pool.go` — `ExifToolPool`, goroutine-safe checkout
