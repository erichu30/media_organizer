# Conventions

Naming, code style, and patterns used throughout this codebase.

---

## Package Structure

| Package | Role |
|---|---|
| `src/internal` | EXIF extraction only — no CLI, no file I/O |
| `src/cmd` (package `main`) | Everything else: CLI, transfer, signal handling |

Do not put application logic in `src/internal`. It exists solely to isolate the exiftool dependency.

---

## Error Returns

`processFile` returns `(reason string, err error)`:
- `reason` is a short human-readable bucket string for `Stats` (e.g. `"no EXIF date"`, `"file operation failed"`).
- `err` is the underlying Go error for logging.
- If there is no error, return `("", nil)`.

Workers read `reason` to increment the correct stats counter. Do not embed structured data in reason strings — they appear in the summary output verbatim.

---

## Interfaces at the Consumer Site

Interfaces are defined where they are consumed, not where the implementation lives:
```go
// in src/cmd/app.go — not in src/internal
type ExifService interface {
    ExtractDate(path string, debug bool, useFileModifyDate bool) (time.Time, string, error)
}
```
This keeps `src/internal` free of knowledge about its callers and makes `App` testable with a fake.

---

## Variable Injection for Tests

When a standard library function needs to be overridable in tests, assign it to a package-level variable:
```go
var osRename = os.Rename
```
Tests override with:
```go
osRename = func(src, dst string) error { return syscall.EXDEV }
```
Use this pattern sparingly — only for things that are genuinely hard to test otherwise (e.g., cross-device errors that require two real filesystems).

---

## Context Propagation

Always propagate `ctx` down the call chain:
```
App.Run(ctx) → worker(ctx, ...) → processFile(ctx, ...) → transfer*(ctx, ...)
```
Never create a `context.Background()` inside a function that receives a `ctx` parameter. The one exception is `cleanupRemoteFile`, which intentionally uses a fresh 30 s context so cleanup runs even after the run context is cancelled.

---

## Concurrency Patterns

| Need | Pattern |
|---|---|
| Goroutine-safe cache | `sync.Map` |
| Reusable buffers | `sync.Pool` |
| Per-run counters | `atomic.Int64` |
| Mutex-guarded map | `sync.Mutex` + plain `map` |
| Worker pool | buffered `chan string` + `sync.WaitGroup` |

Do not use `sync.Mutex` to guard something that could be `sync.Map` or `atomic`.

---

## Signal Handling — `main()` as a Thin Wrapper

```go
func main() { os.Exit(run()) }
```
`main()` does nothing except call `run()` and pass the exit code. All setup, cleanup, and defers live in `run()`. This pattern ensures defers always execute — `os.Exit` from `main()` directly would skip them.

---

## Logging

- Default level: `logrus.InfoLevel`
- `-debug` flag: `logrus.DebugLevel` + full EXIF JSON per file
- All log output goes to `sortbydate.log` (append) in the working directory
- Use `logrus.WithField` / `logrus.WithFields` — never `fmt.Println` for operational output
- Stats summary goes to `fmt.Fprintf(os.Stdout, ...)` via `Stats.print` — not to the logger

---

## File Naming — Platform-Specific Code

Platform-specific files use both a filename suffix **and** a build tag:

```go
// fileutil_darwin.go
//go:build darwin

// fileutil_other.go
//go:build !darwin
```

Both the suffix and the tag are required. The filename suffix documents intent to readers; the build tag is what the compiler actually uses.

---

## Test Patterns

- App-level integration tests use a `testify/suite` (`TestAppSuite`) so per-test setup/teardown is DRY.
- Unit tests (EXIF parsing, config) use plain `go test` with table-driven cases.
- Tests that require external binaries (`exiftool`) call `t.Skip` if the binary is absent.
- Temp directories: always use `t.TempDir()` — it cleans up automatically even on failure.
- Fake EXDEV injection: override `osRename` at the start of the test and restore with `defer`.

---

## CLI Flags

- Use single-hyphen flags (`-dry-run`, `-copy`, `-workers N`) — not double-hyphen.
- Boolean flags are `-flag` (no value), not `-flag=true`.
- Default values are documented in the flag's usage string.
- New flags must have a corresponding test in `sort_by_date_test.go` or `estimate_test.go`.

---

## Commit Messages

Follow the existing style from `git log`:
```
Add <feature/component>
Fix <what>
Refactor <what>
```
One-line summary. No period at the end. Imperative mood. Reference the TASKS.md entry if the change implements a task.
