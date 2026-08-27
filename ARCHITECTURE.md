# Architecture

## Package Layout

```
media_organizer/
├── src/
│   ├── internal/           # EXIF extraction layer
│   │   ├── exiftool_service.go   # single exiftool subprocess wrapper
│   │   └── exiftool_pool.go      # N-instance pool, goroutine-safe
│   └── cmd/                # main application (package main)
│       ├── sort_by_date.go       # main(), run() int, setupLogging, log rotation
│       ├── app.go                # App, Stats, ExifService interface, Run, worker, collectFiles, printHints
│       ├── config.go             # Config, flags, validateConfig, validateDependencies, validatePaths, SSH path conversion
│       ├── process.go            # processFile, extractDate, transferLocal, transferRemote, circuit breaker
│       ├── conflict.go           # -on-conflict policy, destination probing, name claiming
│       ├── fileops.go            # isMediaFile, isExcludedDir, copyFile
│       ├── fileutil_darwin.go    # preserveTimestamps (darwin: birth time via setattrlist)
│       ├── fileutil_other.go     # preserveTimestamps (non-darwin: mtime via os.Chtimes)
│       ├── failure_log.go        # FailureLogger (NDJSON, buffered, mutex-guarded)
│       └── estimate.go           # pre-run info, time estimation (-estimate flag)
├── docs/
│   ├── perf_improvements.md
│   └── perf_estimation.md
└── scripts/                # one-off shell utilities
```

## Data Flow

```
main()
  └── run() int                            ← 0 clean · 1 failure/interrupt · 2 bad flags
        ├── NewConfig()          → Config  (SSH path normalised here)
        ├── validateConfig()               → exit 2 on an unusable flag value
        ├── setupLogging()                 → logrus now writes to the log file, not the terminal
        ├── validateDependencies()         → exiftool, and rclone when remote
        ├── validatePaths()                → input readable, local output creatable
        ├── open ExifToolPool (N services)
        ├── signal.NotifyContext (SIGINT/SIGTERM/SIGHUP)
        └── App.Run(ctx)
              ├── collectFiles()  → paths, sizes  (excluded dirs pruned here)
              ├── printPreRunInfo()          → counts, mode, optional -estimate probe
              ├── feeder goroutine → jobs channel   (stops on ctx.Done)
              └── N worker goroutines
                    ├── ctx.Err()? → count as "not attempted", skip     ← cancellation drain
                    └── processFile(ctx, path)
                          ├── ExifPool.ExtractDate()     → time.Time, tag
                          ├── resolveTarget()            → final path + action
                          │     ├── actionSkipIdentical  → same-sized file already there
                          │     ├── actionSkipConflict   → -on-conflict=skip
                          │     └── actionTransfer       → possibly a "_1" name
                          ├── dry-run? → log intent and stop (no mkdir)
                          ├── dirCache.LoadOrStore()     → mkdir once per YYYY/MM (local only)
                          └── transfer
                                ├── local move:   os.Rename → EXDEV? → copy+delete
                                ├── local copy:   io.CopyBuffer (1 MiB pool) + preserveTimestamps
                                └── remote:       rclone moveto/copyto subprocess
```

## Key Design Decisions

### Destination Conflict Resolution
Every destination path passes through `resolveTarget` (`conflict.go`) before any transfer runs. Cameras restart their file counters, so two `IMG_0001.jpg` from the same month resolve to the same `YYYY/MM/IMG_0001.jpg` — and `os.Rename`, `os.Create`, and `rclone moveto` all replace the destination silently.

`-on-conflict` chooses the policy: `rename` (default, appends `_1`, `_2`, …), `skip`, `overwrite`, `fail`.

**Name claiming before probing.** `claimIfFree` reserves the candidate path in `app.claimed` (a `sync.Map`) *before* it looks at the filesystem, and releases it only if the destination turns out to be occupied. The reverse order is a race: two workers resolving the same basename simultaneously both see "nothing there" and both take the same path.

**Size means already-transferred.** Whatever the policy, a destination file of the same size is reported as `actionSkipIdentical` and left alone. Without it, a second `-copy` pass over the same input would produce `photo_1.jpg`, then `photo_2.jpg`. The heuristic is size-only — see [TASKS.md](TASKS.md) for the trade-off and the hash-verification option.

**One listing per remote directory.** `remoteDirIndex` caches `rclone lsf` output per `YYYY/MM` directory behind a `sync.Once`, so a 500-file run over 4 months costs 4 listings, not 500 probes. Errors are cached too — a remote that cannot be listed will not accept transfers either, and `recordRemoteFailure` lets the circuit breaker end the run.

### Startup Validation Order
`run()` validates in a fixed order, and the order carries meaning:

| Step | Failure exit | Why here |
|---|---|---|
| `validateConfig` | 2 | Pure flag-value checks; no I/O, no side effects yet |
| `setupLogging` | 1 | **After this, logrus writes to a file, not the terminal** |
| `validateDependencies` | 1 | exiftool / rclone presence |
| `validatePaths` | 1 | Input readable, local output creatable (this one creates the output dir) |

Dependencies are checked separately from paths on purpose: when the exiftool check lived at the top of `validatePaths`, a machine without exiftool reported "exiftool not found" for a missing input directory too, and the real problem stayed hidden.

**Everything after `setupLogging` must print to stderr as well as log.** A `logrus.Fatal` past that point leaves the terminal completely silent.

### Exit Codes
`run()` returns the process exit code, and it is part of the tool's contract — cron jobs and shell scripts have no other way to notice a run that moved nothing.

| Code | Meaning |
|---|---|
| 0 | Every file handled; no failures, no interruption |
| 1 | Any per-file failure, or the run was interrupted / circuit-broken |
| 2 | The command line was rejected before any work started |

### Cancellation Semantics
The feeder stops queueing on `ctx.Done()`, but the jobs channel still holds up to `-buffer` entries. The worker therefore checks `ctx.Err()` at the top of every iteration and counts the remainder as *not attempted* rather than attempting them.

Attempting them is not merely wasteful: each would fail instantly against the cancelled context, and each failure would then run `cleanupRemoteFile` on a fresh, uncancellable 30-second context. That combination turned a Ctrl-C into a multi-second hang and a summary blaming files that were never touched.

`Stats.print` derives the not-attempted count by subtraction, so the summary always accounts for every media file found — including the ones the feeder never dispatched.

### ExifTool Pool
`go-exiftool` starts a persistent `exiftool` subprocess per instance. The subprocess is not goroutine-safe, so each `ExifToolService` wraps one with a `sync.Mutex`. `ExifToolPool` holds N services in a buffered channel — workers check out an instance, do the work, and return it via deferred function even on panic.

**Why N instances instead of one:** Allows concurrent EXIF reads matching the worker count, avoiding the single subprocess becoming a bottleneck.

### SSH Path Normalization at Config Time
SSH `user@host:/path` is detected by `isSFTPPath` and converted to rclone on-the-fly SFTP syntax (`:sftp,host=...,user=...[,key_file=...]:path`) in `NewConfig` — before any other code touches `OutputPath`. After `NewConfig` returns, every code path sees a uniform rclone-style path and `IsRemote = true`.

**Why at config time:** Centralizes the conversion so `processFile`, `transferRemote`, and the circuit breaker never need to know whether the original input was SSH shorthand or a configured rclone remote.

### EXDEV Cross-Device Move Fallback
`os.Rename` returns `EXDEV` (errno 18) when source and destination are on different filesystems. The code catches this and falls back to `io.CopyBuffer` + `os.Remove`. If context is cancelled after copy but before delete, the delete is skipped to preserve the source.

**Why not always copy+delete:** `os.Rename` is atomic on the same filesystem, which is faster and avoids data loss if the process is killed mid-transfer.

### `var osRename = os.Rename` Injection Point
The package-level variable lets tests inject a fake `EXDEV` error without needing two real mounted filesystems.

### Timestamp Preservation Split by Platform
`fileutil_darwin.go` uses `unix.UtimesNano` + `setattrlist(2)` to restore `mtime`, `atime`, and birth time. `fileutil_other.go` uses `os.Chtimes` for `mtime` only. Timestamp failure is non-fatal so copies to filesystems that don't support birth-time writes (SMB, NFS) still succeed.

**Build constraint:** The split is enforced by `//go:build darwin` / `//go:build !darwin` tags, not filename alone.

### `main()` as Thin Wrapper
`main()` calls `run() int` and passes the return value to `os.Exit`. This ensures all deferred cleanup (pool close, failure log flush) always executes — `os.Exit` called directly from main skips defers.

### Signal Handling with Two-Level Context
`run()` creates a `signal.NotifyContext` (top-level), then a child `runCtx` cancelled by `runCancel`. Workers and the feeder use `runCtx`. The circuit breaker fires `runCancel()` to stop the run without affecting signal state. A second signal re-registers the default OS handler so users can force-kill if needed.

### Remote Circuit Breaker
`consRemoteFailures atomic.Int64` counts consecutive remote failures. At threshold, `runCancel()` is called. Reset to 0 on any successful transfer. Threshold 0 disables the breaker. After **any** rclone failure, `cleanupRemoteFile` runs `rclone deletefile --retries 1` in a fresh 30 s context to remove partial remote files.

`breakerFired atomic.Bool` gates the announcement to the first crossing only. Workers draining in-flight transfers keep incrementing the counter, and each one used to repeat the warning — a hundred near-identical lines scrolling past the summary.

Destination-listing failures also call `recordRemoteFailure`: a remote that cannot be listed will not accept transfers either, so the breaker should end the run rather than let every file fail one at a time.

### First-Error Reporting
`remoteErrShown atomic.Bool` lets exactly one rclone failure per run print its own output to stderr (`reportRemoteError` / `reportDestListError`). Before this, the terminal showed `file operation failed: 300` while the line that explained it — an unconfigured remote name, a rejected SSH key — sat in the log file. One worked example diagnoses it; 300 copies would bury the summary.

### `dirCache sync.Map`
Each `YYYY/MM` directory is created at most once per run. Workers perform a `LoadOrStore` check before calling `os.MkdirAll`. This is safe under concurrent workers and avoids redundant syscalls.

The `MkdirAll` sits **after** the dry-run check in `processFile`, so a dry run leaves no empty `YYYY/MM` skeleton behind. Remote destinations skip it entirely — rclone creates what it needs.

### Excluded Directories
`isExcludedDir` (`fileops.go`) prunes directories whose contents are full of media-extension files that are not the user's photos: Synology `@eaDir` (one `SYNOPHOTO_THUMB_*.jpg` per real photo), trash and recycle folders, and `.photoslibrary` / `.lrdata` bundles that come apart if reorganized. Matching is by exact name plus a lowercase suffix list.

### Log Rotation
The log is append-only and writes roughly one line per file, so it grows for the life of an install. `rotateLogIfLarge` renames it to `<path>.1` past 50 MB at the start of a run. `-log` moves the file somewhere other than the working directory — useful when the tool runs from a different directory each time, or from a container whose working directory is a mounted volume.

### 1 MiB Copy Buffer Pool
`copyBufPool sync.Pool` allocates 1 MiB buffers for `io.CopyBuffer`. Pooling avoids repeated allocation on large batches. Buffer size was benchmarked — see `docs/perf_improvements.md`.

### FailureLogger (NDJSON, opt-in)
`-failure-log auto` opens a timestamped `.ndjson` file per run with `O_TRUNC`. A `bufio.Writer` (64 KiB) + `json.Encoder` guarded by a mutex allows concurrent writes from the worker pool. The buffer is flushed via deferred `Close()` in `run()`.

### EXIF Tag Priority
`DateTimeOriginal` → `CreateDate` → `DateCreated` → `FileModifyDate` (only if `-use-file-modify-date` is set). `-only-datetimeoriginal` short-circuits to skip any file missing `DateTimeOriginal` specifically.
