# TODO

Design analysis for work that is planned but not built. Each entry states the problem,
lays out the options considered, and marks a recommendation.

**Before implementing anything here, read the entry first.** The analysis is the
expensive part; re-deriving it wastes more time than reading it. If you disagree with a
recommendation, say so and why — do not silently pick a different option.

**When an entry ships,** move it to *Shipped* below with the commit that did it, and
delete the analysis only if nothing in it is still true.

| Task | Status |
|---|---|
| [SSH Session Reuse / Batch rclone](#ssh-session-reuse--batch-rclone-optimization) | Not implemented — **design needs revision**, see note |
| [Source Disk Disconnect Resilience](#source-disk-disconnect-resilience) | Not implemented |
| [Content Verification for Duplicate Detection](#content-verification-for-duplicate-detection) | Not implemented |

---

## Shipped

Kept short. The reasoning lives in [ERRORS.md](ERRORS.md) and [ARCHITECTURE.md](ARCHITECTURE.md);
this list exists so nobody re-solves a solved problem.

| What | Where | Commit |
|---|---|---|
| Destination conflict policy (`-on-conflict`) — duplicate basenames no longer overwrite | `conflict.go` | `79cb430` |
| Flag validation — `-workers 0` hang, negative `-buffer` panic | `validateConfig` in `config.go` | `79cb430` |
| Startup errors reach stderr, not just the log file | `run()` in `sort_by_date.go` | `79cb430` |
| Exit codes 0/1/2 | `run()` in `sort_by_date.go` | `79cb430` |
| Cancellation drain — Ctrl-C stops in seconds, queued files reported as not attempted | `worker` in `app.go` | `79cb430` |
| Circuit breaker announces once | `recordRemoteFailure` in `process.go` | `79cb430` |
| Local paths containing `:` no longer routed through rclone | `isRclonePath` in `config.go` | `79cb430` |
| NAS / photo-library directories excluded from the walk | `isExcludedDir` in `fileops.go` | `79cb430` |
| Log rotation and `-log` | `sort_by_date.go` | `79cb430` |
| Dependency checks split from path checks | `validateDependencies` in `config.go` | `e4af12e` |

---

## SSH Session Reuse / Batch rclone Optimization

**Status:** Not implemented

### Problem

In remote mode, each file transfer spawns a separate `rclone moveto`/`rclone copyto` subprocess. Every subprocess performs a full SSH handshake (~300–500 ms per connection). For a directory with hundreds of files this means:

```
142 files → 142 SSH handshakes → ~60–70 s of pure handshake overhead
```

The actual data transfer is fast; the connection setup cost dominates for small files and any collection larger than a few dozen files.

### Option A — SSH ControlMaster (not viable)

Configure OpenSSH ControlMaster multiplexing via `~/.ssh/config`:

```
Host nas.example.lan
    ControlMaster auto
    ControlPath ~/.ssh/control-%C
    ControlPersist 60s
```

**Why it won't help:** rclone's SFTP backend uses its own Go SSH client (`golang.org/x/crypto/ssh`), not the system `ssh` binary, so it never reads `ControlPath` or reuses an existing socket. No code change can make this work without replacing rclone's transport.

### Option B — Batch by destination folder via `--files-from` (recommended, needs revision)

> **This design predates `-on-conflict` and no longer composes with it.** Read this box
> before implementing.
>
> **The conflict.** `--files-from` transfers a list of sources into one destination
> folder **under their original basenames**. rclone has no per-file rename within a
> batch. So `-on-conflict rename`, which is the default, cannot be expressed as a single
> batched call: a file that needs to become `IMG_0001_1.jpg` has to leave the batch.
>
> **Suggested shape.** Resolve every destination with `resolveTarget` first, then split
> the group: files whose final path equals their original basename go in the batch; files
> that were renamed, and files whose destination directory differs, fall back to
> individual `moveto`/`copyto` calls. In a typical import the renamed set is a small
> fraction, so most of the handshake saving survives.
>
> **Two things already built that help.**
> - `remoteDirIndex` (`conflict.go`) already lists each `YYYY/MM` directory exactly once
>   per run via `rclone lsf` and caches it. Batching needs that same listing to decide
>   conflicts, so reuse the cache rather than adding a second probe. It also makes
>   `--no-traverse` the right flag: the listing is already in hand.
> - `-on-conflict skip` maps directly onto rclone's native `--ignore-existing`, so that
>   policy costs nothing extra in batch mode.
>
> **Do not** implement batching by dropping the conflict check for remote destinations.
> That reintroduces the silent-overwrite data loss that `conflict.go` exists to prevent.


Instead of one rclone process per file, group source files by their `YYYY/MM` destination and issue one rclone invocation per unique destination folder.

**rclone flags used:**

| Flag | Purpose |
|---|---|
| `--files-from <file>` | Read the list of source filenames (relative to `--src-dir`) from a file |
| `--no-traverse` | Skip scanning the remote; trust the provided list |

**Algorithm:**

1. After EXIF extraction (but before transfer), group `(srcPath, dstFolder)` pairs by `dstFolder` (`YYYY/MM` relative to the output root).
2. For each group, write source basenames to a temp file (one per line).
3. Run one rclone `move`/`copy` with `--files-from <tempfile>` per group.
4. Delete the temp file after the call.

**Result:** 142 files all destined for `2026/02/` → 1 SSH handshake instead of 142.

**Implementation notes:**

- The current pipeline (walk → EXIF → transfer per file) needs restructuring: EXIF extraction must complete for all files before batching. Consider a two-pass design or a collect-then-batch stage.
- **Per-file failure tracking:** `rclone move` with `--files-from` fails the whole batch on any error. To track individual failures, parse rclone's `--log-level DEBUG` output for per-file lines, or fall back to individual retries for any file rclone reports as failed.
- **Circuit breaker adaptation:** The current breaker counts consecutive per-file remote failures. In batch mode, a single SSH disconnect may fail a whole batch. The threshold should be treated as consecutive batch failures (or converted to a per-file count by reporting all files in the failed batch as individual failures).
- **Dry-run:** Pass `--dry-run` to rclone when `-dry-run` is set; behavior is unchanged.
- **Progress reporting:** Per-file progress bars won't work in batch mode. Consider switching to a batch-level counter or using rclone's `--progress` output.
- **Partial-file cleanup on batch failure:** `cleanupRemoteFile` is per-file; adapt to iterate over all files in the batch when a batch rclone call fails.

**Expected speedup:** For a typical run of 100–500 files spread across a small number of `YYYY/MM` folders (e.g., 3–6 unique months), handshake overhead drops from minutes to seconds.

---

## Source Disk Disconnect Resilience

**Status:** Not implemented

### Problem

When the source is an external disk (USB, SD card, NFS mount) that disconnects mid-run, the current design has two gaps:

1. **Misleading failure reason** — `ExtractDate` (and `copyFile`) return I/O errors that the code maps to `"no EXIF date"` or `"file operation failed"`. There is no distinction between "exiftool couldn't find a date tag" and "the disk is gone."
2. **No source-side circuit breaker** — the existing circuit breaker only fires on consecutive *remote destination* failures. If the source disk vanishes and 300 files are queued, all 300 fail one by one before the run stops.
3. **Walk silently continues on I/O errors** — `collectFiles` logs a warning and continues, collecting paths that may no longer be reachable.

Data integrity is unaffected (no corruption, no silent loss — see analysis in code review). The problems are diagnostic quality and wasted time.

---

### Problem 1 — Misleading failure reason

#### Option 1A — Classify I/O errors at the call site (recommended)

In `extractDate` and `copyFile`, inspect the error with `errors.Is` / `errors.As` against `syscall.EIO`, `syscall.ENXIO`, `syscall.ENODEV`, and `os.ErrNotExist`. Return `"source I/O error"` instead of `"no EXIF date"` or `"file operation failed"`.

```go
func isSourceIOError(err error) bool {
    return errors.Is(err, syscall.EIO) ||
        errors.Is(err, syscall.ENXIO) ||
        errors.Is(err, syscall.ENODEV) ||
        errors.Is(err, os.ErrNotExist)
}
```

| | |
|---|---|
| **Pro** | Targeted, minimal change; correct reason shown in summary and failure log |
| **Pro** | No extra syscall per file; zero overhead on the happy path |
| **Con** | Must cover all relevant `syscall` errno values (platform-specific on Windows) |
| **Con** | `exiftool` wraps the real error inside its own message string; `errors.Is` may not reach the underlying errno without unwrapping |

#### Option 1B — Pre-check file accessibility before processing (TOCTOU-prone)

Call `os.Lstat(path)` at the top of `processFile`. If it fails with an I/O error, return `"source I/O error"` immediately, before invoking exiftool.

| | |
|---|---|
| **Pro** | Simple; works even when exiftool swallows the underlying errno |
| **Pro** | Produces a clear message without inspecting exiftool output |
| **Con** | TOCTOU race — disk can disconnect between `Lstat` and `os.Open` inside exiftool |
| **Con** | Extra `Lstat` syscall per file on the happy path (~negligible, but non-zero) |
| **Con** | Does not help when the disk disconnects *during* the copy, only before it starts |

---

### Problem 2 — No source-side circuit breaker

#### Option 2A — Add a separate source failure counter (recommended)

Mirror the existing `consRemoteFailures` / `RemoteFailThreshold` pattern with `consSourceFailures atomic.Int64` and a new `-source-fail-threshold` flag (default 5). Fire `runCancel()` when consecutive source I/O errors reach the threshold.

| | |
|---|---|
| **Pro** | Consistent with existing remote circuit breaker; tested pattern already in place |
| **Pro** | Independent tunability — source and remote thresholds may warrant different values |
| **Pro** | Reset on success, so transient single-file errors don't abort the run |
| **Con** | New flag adds surface area; users must understand two separate thresholds |
| **Con** | Requires distinguishing source I/O errors from ordinary EXIF failures (depends on Option 1A/1B) |

#### Option 2B — Unified I/O failure counter

Merge source and remote failures into a single `consIOFailures` counter with one threshold flag. Any `"source I/O error"` or `"file operation failed"` increments the same counter.

| | |
|---|---|
| **Pro** | Single flag, simpler mental model for users |
| **Con** | A burst of ordinary remote failures could mask a source-disk event, or vice versa |
| **Con** | Less diagnostic clarity — summary can't distinguish "remote died" from "disk ejected" |

#### Option 2C — Periodic mount-point liveness check

Spawn a background goroutine that `os.Stat`s the input root every N seconds. On failure, cancel the run context.

| | |
|---|---|
| **Pro** | Proactive detection independent of per-file failures |
| **Pro** | Stops the feeder before more files are dispatched, not after they already failed |
| **Con** | Complex; requires deriving the mount point from an arbitrary input path |
| **Con** | Introduces a background goroutine that must be coordinated with the worker lifecycle |
| **Con** | Still races: disk can disconnect between the check and the next file open |

---

### Problem 3 — Walk silently continues on I/O errors

#### Option 3A — Abort walk on source I/O errors

Return the error from the `WalkDir` callback (instead of `nil`) when an I/O error is encountered on the source. This stops the walk immediately.

| | |
|---|---|
| **Pro** | Fast fail; no stale paths queued for workers to churn through |
| **Pro** | Trivial change — one `return err` instead of `return nil` |
| **Con** | Any single I/O error anywhere in the tree aborts the entire walk, even if only one subdirectory is affected |
| **Con** | Makes the tool less tolerant of partially unreadable source trees (e.g., single corrupt directory) |

#### Option 3B — Validate queued paths in the feeder loop

In the feeder loop (before sending to `jobs`), call `os.Lstat(path)` and skip with a logged warning if it fails.

```go
for _, path := range paths {
    if _, err := os.Lstat(path); err != nil {
        app.stats.addFailure("source I/O error")
        continue
    }
    select {
    case jobs <- path:
    case <-runCtx.Done():
        ...
    }
}
```

| | |
|---|---|
| **Pro** | Prevents dispatching paths that are already known-dead at feed time |
| **Pro** | Does not abort the whole walk; other files still process |
| **Con** | Extra `Lstat` per file in the feeder (single-threaded; may be a bottleneck for very large collections) |
| **Con** | TOCTOU: disk can still disconnect between `Lstat` and the worker's `os.Open` |
| **Con** | Does not distinguish transient errors from permanent disconnect |

---

### Recommended combination

| Problem | Pick |
|---|---|
| Misleading reason | **1B** (pre-check `Lstat` in `processFile`) — simpler, survives exiftool error wrapping |
| Source circuit breaker | **2A** (separate `-source-fail-threshold` flag) — consistent with existing pattern |
| Walk continues on I/O | **3B** (feeder-loop `Lstat` validation) — non-aborting, pairs naturally with 2A |

---

## Content Verification for Duplicate Detection

**Status:** Not implemented

### Problem

`resolveTarget` (`conflict.go`) treats a destination file **of the same size** as the
same file, reports `actionSkipIdentical`, and leaves the source alone. That is what
keeps re-runs idempotent — without it a second `-copy` pass would produce `photo_1.jpg`,
then `photo_2.jpg`.

Size is a weak identity signal. Two genuinely different photos that happen to share a
byte count are treated as the same file, and the incoming one is never transferred.

**The failure is safe but not silent-free:**

| | Behaviour |
|---|---|
| Data loss | None. In move mode the source is *kept*, not deleted |
| Visible? | Counted under `Already there` in the summary |
| Cost to the user | A file they expected to be filed is still sitting in the input |

The likelihood is low for camera originals (JPEG sizes vary widely) and higher for
generated content — thumbnails, exported-at-fixed-quality batches, screenshots at a
fixed resolution.

### Why size only, today

The check has to work for both destinations:

- **Local:** `os.Lstat` gives size for free — it is already being called to test existence.
- **Remote:** `rclone lsf --format sp` returns size in the directory listing that
  `remoteDirIndex` already fetches. Zero extra round trips.

Anything stronger costs a full read of both files.

### Option 1 — Opt-in hash verification (`-verify hash`)

Add a flag that, **only when sizes match**, hashes both sides before deciding.

- Local: read both files, compare (e.g. BLAKE3 or SHA-256).
- Remote: `rclone hashsum` — but note not every backend supports every hash, and some
  (crypt, some object stores) may force a download.

**Cost:** one extra full read per size-collision, not per file. In a re-run over an
already-organized library that is *every* file, which is the expensive case exactly when
the feature matters least.

**Mitigation:** hash only the first and last 64 KiB plus the size. Cheap, and for real
media files a collision needs deliberate construction.

### Option 2 — Compare modification time alongside size (recommended first step)

`copyFile` already calls `preserveTimestamps`, and rclone preserves mtime on most
backends, so a file this tool transferred should carry the source's mtime. Requiring
`size == size && mtime == mtime` is nearly free:

- Local: `os.Lstat` already returns both.
- Remote: `rclone lsf --format spt` adds mtime to the listing already being fetched.

**Cost:** one extra character in the `lsf` format string.

**Caveat:** backends with coarse or absent mtime support (some object stores, SMB in
certain configurations) would start reporting false conflicts and renaming files that
are in fact already there. Needs a fallback to size-only when the listing has no usable
timestamp — an empty or zero mtime must not be compared.

### Option 3 — Leave it, document it

The current behaviour is already the safe direction: it declines to transfer rather than
risking a duplicate, and never deletes. Documented in
[ARCHITECTURE.md](ARCHITECTURE.md#destination-conflict-resolution) and [ERRORS.md](ERRORS.md).

### Recommendation

**Option 2 first** — it removes most of the false-identical cases for the price of one
extra field in a listing already being fetched, and it degrades to today's behaviour
when mtime is unavailable. Revisit Option 1 only if a real collision is observed in
practice.

Before building either, get a measurement: run over a real library and count how many
files land in `Already there`, and how many of those are genuinely the same file.
