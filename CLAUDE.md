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

See [ARCHITECTURE.md](ARCHITECTURE.md).

## Reference Docs

| File | Read When |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Touching package structure, adding a new transfer path, or understanding a design decision |
| [ERRORS.md](ERRORS.md) | Editing `fileops.go`, `exiftool_pool.go`, `process.go`, `fileutil_*.go`, or any transfer/signal path |
| [CONVENTIONS.md](CONVENTIONS.md) | Adding a new function, flag, test, or unsure about naming/pattern |
| [TASKS.md](TASKS.md) | Implementing a new feature — check for existing analysis before designing |
| [SKILLS.md](SKILLS.md) | Choosing which skill to invoke for a task |
