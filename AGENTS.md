# AGENTS.md

Working agreement for AI coding agents in this repository. Claude Code reads this via
`CLAUDE.md`; Codex and other tools read it directly.

Personal setup — your machine's paths, your remotes, tooling only you have installed —
belongs in `CLAUDE.local.md`, which is gitignored. Do not add it here.

## Project

`media_organizer` is a Go CLI that files photos and videos into `YYYY/MM` directories by
EXIF date. Destinations can be local or remote via rclone.

Runtime dependencies, both external binaries:

- `exiftool` — reads EXIF metadata. Required for every run.
- `rclone` — remote transfers only. Accepts `remotename:path` or `user@host:/path`
  (SSH shorthand, converted to rclone SFTP on-the-fly syntax in `NewConfig`).

## Commands

```bash
./build.sh                                    # → build/sort_by_date
go build -o ./build/sort_by_date ./src/cmd/
docker build -t media-organizer .

go test ./...
go test -race ./...
go test ./src/cmd/ -run TestAppSuite                          # one suite
go test ./src/cmd/ -run "TestAppSuite/TestProcessFile_LocalMove"  # one case
go test ./src/internal/ -run TestParseExifDate

./build/sort_by_date -i <dir> -o <dir> -dry-run
./build/sort_by_date -i <dir> -o <remote:path> -copy -estimate
```

## Behaviour Contracts

Invariants this codebase depends on. Breaking one does not fail a test — it produces a
tool that quietly does the wrong thing. Each has a matching entry in [ERRORS.md](ERRORS.md).

| Contract | Consequence of breaking it |
|---|---|
| **Every transfer path goes through `resolveTarget`** before it writes | Duplicate-filename data loss returns. `os.Rename`, `os.Create`, and `rclone moveto` all replace the destination silently |
| **`resolveTarget` claims the name before probing the filesystem** | Two workers resolving the same basename both see "nothing there" and pick the same path |
| **No `logrus.Fatal` after `setupLogging`** | logrus is redirected to the log file there; a fatal past it exits 1 with a blank terminal. Print to stderr *and* log |
| **`os.Exit` only from `main()`** | `run()` returns the code so deferred cleanup — pool shutdown, failure-log flush — actually runs |
| **Exit codes are 0 / 1 / 2 as documented** | Cron jobs and scripts have no other signal. 0 means every file was handled |
| **`-dry-run` writes nothing** — files and directories both | It is the command users reach for when unsure. `MkdirAll` sits after the dry-run check for this reason |
| **Cancelled work is not failure** | Queued-but-unreached files are `Not attempted`. Counting them as failures blames files never touched |
| **`Stats.print` accounts for every media file found** | The not-attempted count is derived by subtraction; a new outcome counter must join that subtraction |

## Code Conventions

**Packages.** `src/internal` is EXIF extraction only — no CLI, no file I/O. It exists to
isolate the exiftool dependency. Everything else lives in `src/cmd` (package `main`).

**Error returns.** `processFile` returns `(reason string, err error)`. `reason` is a short
bucket string that appears verbatim in the summary (`"no EXIF date"`,
`"file operation failed"`); `err` is the underlying error for the log. Success is
`("", nil)`. The sentinel `reasonSkipped` means "deliberately not transferred" — the
worker must not count it as a success.

**Interfaces at the consumer site.** `ExifService` is declared in `src/cmd/app.go`, not in
`src/internal`, so `App` is testable with a fake and `src/internal` knows nothing about
its callers.

**Context propagation.** `App.Run(ctx) → worker(ctx) → processFile(ctx) → transfer*(ctx)`.
Never create a `context.Background()` inside a function that already receives a `ctx`.
The one deliberate exception is `cleanupRemoteFile`, which needs a fresh context so
cleanup still runs after the run context is cancelled.

**Concurrency.**

| Need | Use |
|---|---|
| Goroutine-safe cache | `sync.Map` |
| Reusable buffers | `sync.Pool` |
| Per-run counters | `atomic.Int64` / `atomic.Bool` |
| Mutex-guarded map | `sync.Mutex` + plain `map` |
| Worker pool | buffered `chan string` + `sync.WaitGroup` |

Do not reach for a `sync.Mutex` where `sync.Map` or an atomic would do.

**Platform-specific files** need both a filename suffix and a build tag —
`fileutil_darwin.go` with `//go:build darwin`, `fileutil_other.go` with `//go:build !darwin`.
The suffix documents intent; the tag is what the compiler acts on.

**Test injection.** When a stdlib call must be overridable, assign it to a package-level
variable (`var osRename = os.Rename`, `var rcloneListDir = func(...)`). Use it sparingly —
only for things genuinely hard to test otherwise, like a cross-device error that would
need two real filesystems.

**Tests.** App-level integration tests use the `testify/suite` (`TestAppSuite`). Unit tests
are table-driven. Anything needing a real `exiftool` or `rclone` calls `t.Skip` when the
binary is absent. Always `t.TempDir()`, never a hand-rolled temp path.

**Commit messages.** One-line imperative summary, no trailing period: `Add <what>`,
`Fix <what>`, `Refactor <what>`. Reference the [TASKS.md](TASKS.md) entry when the change
implements one.

## Adding a Flag

1. Field on `Config` (`config.go`) plus the `flag.*Var` registration. Single hyphen
   (`-dry-run`, not `--dry-run`); document the default in the usage string.
2. A range or enum check in `validateConfig` if any value would be unusable. A flag that
   can hang or panic the process must be rejected at startup, not discovered at run time.
3. A test in `sort_by_date_test.go`.
4. Regenerate the `## Usage` block in [README.md](README.md) — it is verbatim `-h` output,
   so run the binary rather than hand-editing.

## Verifying a Change

```bash
gofmt -l ./src      # must print nothing
go vet ./...
go test -race ./...
```

The suite must also pass **without `exiftool` or `rclone` installed** — that is what CI
runs, and it is how a dependency-check ordering bug once reached CI green locally:

```bash
env PATH="$(dirname $(which go)):/usr/bin:/bin" go test ./...
```

## Reference Docs

Read on demand, not by default.

| File | Read when |
|---|---|
| [ARCHITECTURE.md](ARCHITECTURE.md) | Changing package structure, adding a transfer path, or asking why something is built the way it is |
| [ERRORS.md](ERRORS.md) | Editing `conflict.go`, `process.go`, `fileops.go`, `exiftool_pool.go`, `fileutil_*.go`, or any transfer/signal path |
| [TASKS.md](TASKS.md) | Starting a new feature — the analysis may already exist, and one entry carries a warning about a design that no longer composes |
| [README.md](README.md) | User-facing behaviour: flags, output format, recipes |
