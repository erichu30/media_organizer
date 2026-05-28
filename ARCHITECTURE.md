# Architecture

## Package Layout

```
media_organizer/
├── src/
│   ├── internal/           # EXIF extraction layer
│   │   ├── exiftool_service.go   # single exiftool subprocess wrapper
│   │   └── exiftool_pool.go      # N-instance pool, goroutine-safe
│   └── cmd/                # main application (package main)
│       ├── sort_by_date.go       # main() + run()
│       ├── app.go                # App, Stats, ExifService interface
│       ├── config.go             # Config, flag parsing, SSH path conversion
│       ├── process.go            # processFile, dirCache, worker loop
│       ├── fileops.go            # transferLocal, transferRemote, circuit breaker
│       ├── fileutil_darwin.go    # preserveTimestamps (darwin: birth time via setattrlist)
│       ├── fileutil_other.go     # preserveTimestamps (non-darwin: mtime via os.Chtimes)
│       ├── failure_log.go        # FailureLogger (NDJSON, buffered, mutex-guarded)
│       └── estimate.go           # time estimation (-estimate flag)
├── docs/
│   ├── perf_improvements.md
│   └── perf_estimation.md
└── scripts/                # one-off shell utilities
```

## Data Flow

```
main()
  └── run() int
        ├── parse flags → Config
        ├── open ExifToolPool (N workers)
        ├── signal.NotifyContext (SIGINT/SIGTERM/SIGHUP)
        └── App.Run(ctx)
              ├── collectFiles() → []string paths
              ├── feeder goroutine → jobs channel
              └── N worker goroutines
                    └── processFile(ctx, path)
                          ├── ExifPool.ExtractDate()  → time.Time
                          ├── dirCache.LoadOrStore()  → mkdir once per YYYY/MM
                          └── transfer (local or remote)
                                ├── local move:   os.Rename → EXDEV? → copy+delete
                                ├── local copy:   io.CopyBuffer (1 MiB pool) + preserveTimestamps
                                └── remote:       rclone moveto/copyto subprocess
```

## Key Design Decisions

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

### `dirCache sync.Map`
Each `YYYY/MM` directory is created at most once per run. Workers perform a `LoadOrStore` check before calling `os.MkdirAll` (local) or SSH `mkdir -p`. This is safe under concurrent workers and avoids redundant syscalls.

### 1 MiB Copy Buffer Pool
`copyBufPool sync.Pool` allocates 1 MiB buffers for `io.CopyBuffer`. Pooling avoids repeated allocation on large batches. Buffer size was benchmarked — see `docs/perf_improvements.md`.

### FailureLogger (NDJSON, opt-in)
`-failure-log auto` opens a timestamped `.ndjson` file per run with `O_TRUNC`. A `bufio.Writer` (64 KiB) + `json.Encoder` guarded by a mutex allows concurrent writes from the worker pool. The buffer is flushed via deferred `Close()` in `run()`.

### EXIF Tag Priority
`DateTimeOriginal` → `CreateDate` → `DateCreated` → `FileModifyDate` (only if `-use-file-modify-date` is set). `-only-datetimeoriginal` short-circuits to skip any file missing `DateTimeOriginal` specifically.
