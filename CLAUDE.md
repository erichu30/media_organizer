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
./build/sort_by_date -i /path/to/input -o /path/to/output -on-conflict skip
./build/sort_by_date -i /path/to/input -o /path/to/output -log /var/log/sort_by_date.log
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
| [ERRORS.md](ERRORS.md) | Editing `conflict.go`, `process.go`, `fileops.go`, `exiftool_pool.go`, `fileutil_*.go`, or any transfer/signal path |
| [CONVENTIONS.md](CONVENTIONS.md) | Adding a new function, flag, test, or unsure about naming/pattern |
| [TASKS.md](TASKS.md) | Implementing a new feature — check for existing analysis before designing |
| [SKILLS.md](SKILLS.md) | Choosing which skill to invoke for a task |

## Behaviour Contracts

Invariants this codebase depends on. Breaking one of these does not fail a test — it
produces a tool that quietly does the wrong thing. Each has a matching entry in
[ERRORS.md](ERRORS.md).

| Contract | Why |
|---|---|
| **Every transfer path goes through `resolveTarget`** before it writes. | `os.Rename`, `os.Create`, and `rclone moveto` all replace the destination silently. A new transfer path that skips it reintroduces duplicate-filename data loss. |
| **`resolveTarget` claims the name before it probes the filesystem.** | Two workers resolving the same basename simultaneously both see "nothing there". Claim-then-probe is what makes them pick different suffixes. |
| **Nothing calls `logrus.Fatal` after `setupLogging`.** | logrus is redirected to the log file at that point. A fatal past it exits 1 with a completely blank terminal. Print to stderr *and* log. |
| **`os.Exit` is called only from `main()`.** | `run()` returns the code so deferred cleanup — pool shutdown, failure-log flush — actually runs. |
| **Exit codes are 0 / 1 / 2 as documented.** | Cron jobs and scripts have no other signal. 0 means every file was handled. |
| **`-dry-run` writes nothing at all** — no files, no `YYYY/MM` directories. | It is the command users reach for when they are unsure. `MkdirAll` sits after the dry-run check for this reason. |
| **Cancelled work is not counted as failure.** | Queued-but-unreached files are `Not attempted`. Counting them as failures blames files that were never touched. |
| **`Stats.print` accounts for every media file found.** | The not-attempted count is derived by subtraction. A new outcome counter must be added to that subtraction or the summary stops adding up. |

## Adding a Flag

1. Field on `Config` (`config.go`) + `flag.*Var` registration.
2. A range or enum check in `validateConfig` if any value would be unusable — a flag that
   can hang or panic the process must be rejected there, not discovered at run time.
3. A test in `sort_by_date_test.go` (required by [CONVENTIONS.md](CONVENTIONS.md)).
4. Update the `## Usage` block in [README.md](README.md) — it is verbatim `-h` output,
   so regenerate it rather than hand-editing.

## Verifying a Change

```bash
gofmt -l ./src            # must print nothing
go vet ./...
go test -race ./...
```

The tests must also pass **without `exiftool` or `rclone` installed** — that is what CI
runs, and it is how the dependency-check ordering bug got through locally:

```bash
env PATH="$(dirname $(which go)):/usr/bin:/bin" go test ./...
```

Integration tests that need a real binary call `t.Skip` when it is absent.

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
