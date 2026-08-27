# ERRORS — Pitfalls & Gotchas

Every entry here was discovered the hard way. Read before touching related code.

---

## 1. `go-exiftool` is not goroutine-safe — never share an instance across goroutines

`exiftool_service.go` wraps one `exiftool` subprocess. Calling `ExtractDate` concurrently on the same instance silently corrupts results or deadlocks. Always obtain an instance from `ExifToolPool.checkout()` and return it with the deferred `checkin`. Never store or cache an `ExifToolService` pointer outside the pool.

**Symptom:** Garbled dates or a hung run with no timeout.

---

## 2. `os.Rename` silently fails across filesystems with EXDEV

`os.Rename` returns `syscall.EXDEV` (errno 18) when source and destination are on different filesystems (e.g., source on USB, output on SMB mount). The call itself does not error in a way Go's `err != nil` catches differently — you must check `errors.Is(err, syscall.EXDEV)`. The code in `fileops.go` already handles this, but if you add any new move paths, you must do the same.

**Symptom:** Files silently not moved, `transferLocal` returns a mysterious error that looks like a generic rename failure.

---

## 3. SSH paths must be normalized in `NewConfig`, never later

`isSFTPPath` detects `user@host:/path` and `toRcloneSFTPPath` converts it. This happens once in `NewConfig`. After that, `Config.OutputPath` is always a rclone-style path and `IsRemote` is `true`. If you add a new code path that reads `OutputPath` before `NewConfig` runs (e.g., in `main()`), you will get the raw SSH string, not the rclone SFTP syntax, and rclone will fail with a confusing "remote not found" error.

**Symptom:** `rclone: no such remote` or `unknown flag` errors when using SSH shorthand.

---

## 4. `setattrlist` fails on SMB/NFS — timestamp errors must stay non-fatal

`preserveTimestamps` on Darwin calls `setattrlist(2)` to restore birth time. This syscall fails with `ENOTSUP` on network filesystems. The error is logged as a warning and the copy is considered successful. Do **not** change this to a hard failure — it would break every SMB/NFS destination.

**Symptom if you make it fatal:** All copies to network destinations fail with `operation not supported`.

---

## 5. `bufio.Writer` in `FailureLogger` must be flushed before exit

`FailureLogger` uses a 64 KiB `bufio.Writer`. The final records in the buffer are only written when `FailureLogger.Close()` is called. This is deferred in `run()`. If you add an early exit path (e.g., a `log.Fatal` in a worker), the buffer is never flushed and the last batch of failure records is silently lost.

**Rule:** Never call `os.Exit` or `log.Fatal` from worker goroutines. Return errors through channels or the `Stats` struct.

---

## 6. Context cancellation in feeder — always use `select`, never block on channel send

The feeder loop sends paths to the `jobs` channel:
```go
select {
case jobs <- path:
case <-runCtx.Done():
    return
}
```
If you replace the `select` with a plain `jobs <- path`, the feeder goroutine blocks forever when workers exit due to context cancellation, leaking the goroutine and preventing the deferred cleanup from running.

---

## 7. `ExifToolPool.Close()` must be called even on early exits

The pool starts N `exiftool` subprocesses. If `Close()` is never called (e.g., you add an early return path in `run()` before the deferred close), those subprocesses remain alive as zombies until the OS kills them. Always ensure `Close()` is deferred immediately after the pool is created.

---

## 8. Platform build tags must be explicit — filename suffix alone is not enough for tests

`fileutil_darwin.go` and `fileutil_darwin_test.go` use `//go:build darwin`. On non-Darwin hosts, those files are excluded, and `fileutil_other.go` provides the fallback. If you add a new Darwin-only API without the build tag, it will either fail to compile on Linux/Windows CI or silently shadow the other-platform implementation.

**Check:** `grep -r '//go:build' src/cmd/fileutil_*` should show a `darwin` tag and a `!darwin` tag.

---

## 9. `TestExtractDate` in `src/internal` requires a real `exiftool` binary

The test calls the actual `exiftool` subprocess. If `exiftool` is not installed, the test auto-skips with `t.Skip`. If you see the test unexpectedly pass in CI, verify that `exiftool` is actually installed — a skipped test is not a passing test for coverage purposes.

---

## 10. `rclone copyto`/`moveto` on failure leaves a partial file on the remote

When rclone is killed mid-transfer (signal or crash), it may leave a partial file at the destination. `cleanupRemoteFile` runs `rclone deletefile --retries 1` in a fresh 30 s context after any rclone failure. If you refactor `transferRemote`, ensure cleanup still runs on all error paths, including context cancellation.

---

## 11. `sync.Map.LoadOrStore` race in `dirCache` — return value matters

`dirCache.LoadOrStore(dir, true)` returns `(actual, loaded)`. Only the goroutine where `loaded == false` should call `os.MkdirAll`. If you change the `dirCache` logic and check `loaded == true` instead of `false`, every goroutine skips mkdir and directories are never created.

---

## 12. `--files-from` batch mode (not yet implemented) — per-file failure tracking is lossy

See `TASKS.md`. `rclone move --files-from` fails the entire batch on a single error. Parsing per-file status from `--log-level DEBUG` stdout is fragile and version-dependent. Do not assume individual file success/failure from a batch invocation without verifying rclone's current log format.

---

## 13. Destination collisions are silent data loss on every transfer path

`os.Rename`, `os.Create` (inside `copyFile`), and `rclone moveto` all replace the
destination without a word. Two photos named `IMG_0001.jpg` from the same month both
resolve to `2023/05/IMG_0001.jpg`, and the second one used to erase the first while
the summary still counted two successes.

Every destination now goes through `resolveTarget` in `conflict.go` before any
transfer. Do not add a transfer path that bypasses it.

---

## 14. `resolveTarget` must claim a name before checking the filesystem

Two workers can resolve the same basename at the same instant. Neither file exists at
the destination yet, so a naive "does it exist?" check hands both workers the same
path and one of them loses. `claimIfFree` reserves the name in `app.claimed` **first**
and releases it only if the destination turns out to be occupied.

---

## 15. Cancelled work must not be counted as failure

The worker loop reads `ctx.Err()` at the top of every iteration. Without it, the files
already sitting in the jobs channel when Ctrl-C arrives are still attempted: each one
fails instantly against the cancelled context, and each failure then pays for a remote
cleanup on a fresh 30-second context. A 300-file run took 14 extra seconds to exit and
blamed 108 files that had never been touched.

---

## 16. A cached `destIndex` error is returned to every caller

`remoteDirIndex` caches the listing behind a `sync.Once`, errors included. That is
deliberate — one unreachable remote should not be re-probed once per file — but it
means a transient listing failure poisons that directory for the rest of the run. The
circuit breaker is the intended backstop; `recordRemoteFailure` is called on listing
failures for exactly that reason.
