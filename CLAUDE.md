# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`media_organizer` is a Go CLI tool that organizes photo/video files into a `YYYY/MM` directory structure by reading EXIF metadata. It supports local and remote (rclone) destinations.

**External dependencies required at runtime:**
- `exiftool` — reads EXIF metadata from media files
- `rclone` — only needed for remote transfers; accepts `remotename:path` (configured remote) or `user@host:/path` (SSH shorthand, auto-converted to rclone SFTP on-the-fly syntax)

## Commands

```bash
# Build (native)
./build.sh
# or directly:
go build -o ./build/sort_by_date ./src/cmd/

# Build Docker image
docker build -t media-organizer .

# Run all tests
go test -v ./...

# Run a single test or suite
go test -v ./src/internal/ -run TestParseExifDate
go test -v ./src/cmd/ -run TestNewConfig
go test -v ./src/cmd/ -run TestAppSuite                        # whole suite
go test -v ./src/cmd/ -run "TestAppSuite/TestProcessFile_DryRun"  # single suite case

# Run the tool (native)
./build/sort_by_date -i /path/to/input -o /path/to/output
./build/sort_by_date -i /path/to/input -o /path/to/output -dry-run
./build/sort_by_date -i /path/to/input -o myremote:/photos -copy
./build/sort_by_date -i /path/to/input -o root@192.168.1.10:/mnt/nas/photos -ssh-key ~/.ssh/id_ed25519

# Run via Docker
docker run --rm -v /path/to/input:/input -v /path/to/output:/output \
  media-organizer -i /input -o /output
```

## Architecture

See **[ARCHITECTURE.md](ARCHITECTURE.md)** for the full package layout, data-flow diagram, and design-decision rationale.

**Quick summary:**
- `src/internal` — ExifTool pool (goroutine-safe wrapper around `go-exiftool`)
- `src/cmd` — main application: config, worker pool, file transfer (local + rclone), signal handling, failure log

Key files: `app.go` (App/Stats), `config.go` (flag parsing + SSH path normalization), `process.go` (worker/processFile), `fileops.go` (transfer + circuit breaker), `fileutil_darwin.go` / `fileutil_other.go` (timestamp preservation).

## Reference Docs

| File | Purpose |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Package layout, data flow, design decisions |
| [TASKS.md](TASKS.md) | Planned work items with analysis and trade-offs |
| [ERRORS.md](ERRORS.md) | Pitfalls — read before touching related code |
| [CONVENTIONS.md](CONVENTIONS.md) | Naming, patterns, and code style |
| [skill.md](skill.md) | Claude Code skills useful for this project |
