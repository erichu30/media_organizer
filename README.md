# Media Organizer

A command-line tool to organize media files (photos and videos) into a directory structure based on their creation date (YYYY/MM), extracted from EXIF metadata.

## Features

- **Organize by Date**: Automatically moves or copies files into a `YYYY/MM` folder structure.
- **Media-only filtering**: Only processes recognised media files (images and videos); other file types are automatically skipped and counted in the summary.
- **EXIF-based**: Extracts the creation date from EXIF metadata tags (`DateTimeOriginal`, `CreateDate`, `DateCreated`).
- **Fallback to File Date**: Can use the file's modification date if no EXIF date is found.
- **Concurrent Processing**: Uses a worker pool to process files in parallel, significantly speeding up the process for large collections.
- **Flexible Operation**: Supports both moving and copying files.
- **Dry-Run Mode**: Preview the results without making any changes to your files.
- **Remote Sync**: Transfer files to a remote server using `rclone` (supports S3, SFTP, Dropbox, and 40+ backends). Accepts both rclone remote syntax (`remotename:path`) and SSH shorthand (`user@host:/path`). Configurable retry count and circuit breaker for resilience against SSH disconnects.
- **Timestamp Preservation**: When copying, the destination file retains the original `FileModifyDate`, `FileAccessDate`, and (on macOS) `FileCreatedDate` — EXIF file metadata is unchanged.
- **Duplicate-safe**: Two files that share a basename and land in the same `YYYY/MM` folder never overwrite each other. `-on-conflict` picks the policy (`rename` by default, or `skip` / `overwrite` / `fail`), and a same-sized file already at the destination is treated as already transferred so re-runs stay idempotent.
- **Run Summary**: Prints a brief summary to stdout after processing — media file count, success count, failed count broken down by reason, plus renamed / already-present / not-attempted counts so every file is accounted for.
- **Actionable Hints**: When a run finishes with fixable problems (files with no EXIF date, name conflicts), it prints the flag that addresses them.
- **Structured Failure Log**: Optionally writes one NDJSON record per failed file (`-failure-log auto` or `-failure-log <path>`), including path, size, reason, error, and duration.
- **Pre-transfer Info**: Before starting, prints file count and total size. With `-estimate` (remote mode), transfers two sample files (10th and 90th percentile by size) to measure per-file SSH overhead and bandwidth, then shows a projected total transfer time.
- **Signal Handling**: Responds to `SIGINT` (Ctrl-C), `SIGTERM`, and `SIGHUP` with a graceful shutdown — in-progress file operations finish before exit, all buffers are flushed, and the summary is printed with a partial-results warning. A second signal force-quits immediately.
- **Logging**: Keeps a log of all operations in `sortbydate.log` (change with `-log <path>`). The log is rotated to `<path>.1` once it passes 50 MB.

## Dependencies

- **Go** — [golang.org](https://golang.org/)
- **ExifTool** — reads EXIF metadata; [exiftool.org](https://exiftool.org/)
- **rclone** — required only for remote transfers; supports S3, GCS, Dropbox, SFTP, and 40+ other backends. Use either `remotename:path` (configured remote) or `user@host:/path` (SSH shorthand, auto-converted to rclone SFTP on-the-fly). Pass `-ssh-key` to specify the private key file when using SSH shorthand

## Installation

### Option 1: Docker (recommended — no local dependencies required)

```bash
docker build -t media-organizer .
```

```bash
# Basic run
docker run --rm \
  -v /path/to/input:/input \
  -v /path/to/output:/output \
  media-organizer -i /input -o /output

# Persist the log file
docker run --rm \
  -v /path/to/input:/input \
  -v /path/to/output:/output \
  -v /path/to/logs:/data \
  media-organizer -i /input -o /output

# Remote via rclone — mount your rclone config
docker run --rm \
  -v /path/to/input:/input \
  -v ~/.config/rclone:/root/.config/rclone:ro \
  media-organizer -i /input -o myremote:/photos
```

### Option 2: Build from source

```bash
git clone <repository-url>
cd media_organizer
./build.sh
```

This creates `build/sort_by_date`.

## Usage

Verbatim `-h` output — regenerate with `./build/sort_by_date -h` after adding a flag.

```
Usage: ./build/sort_by_date [OPTIONS]

Organize media files by date (YYYY/MM) using EXIF data, with optional remote transfer via rclone.

Required:
	-i <dir>        Input directory
	-o <dir|dest>   Output: local directory, rclone remote (remotename:path),
	                or SSH destination (user@host:/path — auto-converted to rclone SFTP)

Options:
  -buffer int
    	Channel buffer size (default 100)
  -copy
    	Copy instead of move (keep original files)
  -debug
    	Enable debug logging
  -dry-run
    	Show what would be done, without moving/copying files
  -estimate
    	Sample two files to measure destination speed and show a time estimate (remote only; skipped in dry-run)
  -failure-log string
    	Write failed-file records as NDJSON to this path ("auto" = timestamp-based filename)
  -i string
    	Input directory
  -log string
    	Path to the operational log file (default "sortbydate.log")
  -o string
    	Output directory
  -on-conflict string
    	What to do when the destination filename is already taken: rename | skip | overwrite | fail (default "rename")
  -only-datetimeoriginal
    	Only process files with DateTimeOriginal tag
  -remote-fail-threshold int
    	abort after this many consecutive remote failures, 0 to disable (default 5)
  -retries int
    	rclone retry count for transient remote errors (SSH disconnect, timeouts) (default 3)
  -ssh-key string
    	Path to SSH private key for user@host:/path destinations (e.g. ~/.ssh/id_ed25519)
  -use-file-modify-date
    	Use file modify date as a fallback
  -workers int
    	Number of concurrent workers (default 8)

Duplicate filenames:
	Two photos named IMG_0001.jpg taken in the same month both want the same
	destination. -on-conflict decides what happens (default: rename):
	  rename     write the second one as IMG_0001_1.jpg
	  skip       leave the destination alone, leave the source where it is
	  overwrite  replace the file at the destination — data is lost
	  fail       count the file as a failure and move on
	A destination file of the same size is always treated as already transferred,
	so re-running over the same input does not pile up duplicates.

Examples:
	./build/sort_by_date -i /path/to/input -o /path/to/output
	./build/sort_by_date -i /path/to/input -o myremote:/photos -copy
	./build/sort_by_date -i /path/to/input -o root@192.168.1.10:/mnt/nas/photos -ssh-key ~/.ssh/id_ed25519
	./build/sort_by_date -i /path/to/input -o root@192.168.1.10:/mnt/nas/photos -ssh-key ~/.ssh/id_ed25519 -estimate
	./build/sort_by_date -i /path/to/input -o /path/to/output -dry-run
	./build/sort_by_date -i /path/to/input -o /path/to/output -failure-log auto
```

## Duplicate Filenames

Cameras restart their file counters, so `IMG_0001.jpg` recurs across imports. Two such
files shot in the same month both want `2023/05/IMG_0001.jpg`. `-on-conflict` decides
what happens:

| Value | Behaviour |
|---|---|
| `rename` (default) | The second file is written as `IMG_0001_1.jpg`, then `_2`, and so on. Nothing is lost. |
| `skip` | The destination is left alone and the source stays where it is. Counted under "Conflicts skipped". |
| `overwrite` | The destination file is replaced. **Data is lost** — only use this when you know the destination is disposable. |
| `fail` | The file is counted as a failure and the run continues. |

Whatever the policy, a destination file **of the same size** is treated as already
transferred and skipped, so re-running over the same input does not pile up
`_1`, `_2`, `_3` copies of files that are already there.

For remote destinations each `YYYY/MM` directory is listed once per run with
`rclone lsf`, not once per file.

## Common Tasks

Task-first index. Every command assumes the binary is at `./build/sort_by_date`.

### "I want to see what would happen, without touching anything"

```bash
./build/sort_by_date -i ~/Pictures/import -o ~/Photos -dry-run
```

Creates no files and no directories. Read the `Mode` line to confirm move-vs-copy and
the conflict policy before doing it for real.

### "I'm organizing a folder for the first time and don't want to lose the originals"

```bash
./build/sort_by_date -i ~/Pictures/import -o ~/Photos -copy
```

`-copy` keeps the source. Without it the default is **move**, which deletes the original
once the destination is written.

### "Lots of files failed with 'no EXIF date'"

Screenshots, downloaded images, and anything that has been through a messaging app
usually carry no EXIF. Fall back to the filesystem timestamp:

```bash
./build/sort_by_date -i ~/Pictures/import -o ~/Photos -use-file-modify-date
```

Be aware that a file's modification time is when *that copy* was written, which is not
always when the photo was taken.

### "I only trust the camera's own timestamp"

```bash
./build/sort_by_date -i ~/Pictures/import -o ~/Photos -only-datetimeoriginal
```

Anything without a `DateTimeOriginal` tag is counted as a failure and left alone, rather
than filed under a date derived from `CreateDate` or `DateCreated`.

### "I'm re-running over a folder I already organized"

Just run it again. A destination file of the same size is treated as already transferred,
so the second pass reports `Already there` instead of creating `_1` duplicates.

### "I have duplicates and I want to deal with them by hand"

```bash
./build/sort_by_date -i ~/Pictures/import -o ~/Photos -on-conflict skip
```

Conflicting files stay in the input folder. Whatever is left in the input after the run
is exactly the set that needs a decision.

### "Send it to my NAS over SSH"

```bash
./build/sort_by_date -i ~/Pictures/import \
  -o admin@192.168.1.10:/volume1/photo \
  -ssh-key ~/.ssh/id_ed25519 -copy -estimate
```

`-estimate` samples two files first and prints a projected total time — worth it before
committing to a long transfer. Drop it once you know how fast the link is.

### "The transfer keeps dying halfway"

```bash
./build/sort_by_date -i ~/Pictures/import -o mynas:/photo \
  -retries 5 -remote-fail-threshold 10 -failure-log auto
```

`-retries` is passed to rclone for transient errors. `-remote-fail-threshold` aborts the
run after that many consecutive failures instead of grinding through thousands of files
against a dead link. The failure log gives you the exact list to retry.

### "It's running from cron and I need to know if it worked"

```bash
./build/sort_by_date -i /mnt/incoming -o mynas:/photo -log /var/log/sort_by_date.log
echo $?   # 0 = everything transferred · 1 = failures or interrupted · 2 = bad flags
```

### "Something went wrong and I need the details"

Per-file errors are in the log (`sortbydate.log` by default, or `-log <path>`). For a
machine-readable list of exactly what failed and why, add `-failure-log auto`.

---

## Pre-transfer Info

Before any file is moved or copied, the tool prints a quick summary of what it found:

```
--- Pre-transfer ---
  Files      : 142
  Total size : 1.8 GB
  (add -estimate to measure destination speed)
```

### Transfer time estimate (`-estimate`, remote only)

Pass `-estimate` to have the tool transfer two sample files (10th and 90th percentile by size) to the destination under temporary names, measure the round-trip time, and fit a two-parameter model:

```
time = α (per-file SSH overhead) + β × bytes (bandwidth)
```

The temporary probe files are deleted immediately; the main run is unaffected.

```
--- Pre-transfer ---
  Files      : 142
  Total size : 1.8 GB
  Probing    : transferring sample file(s)... done
  Probe      : 245.0 KB + 12.3 MB sampled
  Per-file   : ~380 ms overhead
  Bandwidth  : ~22.4 MB/s
  Estimated  : ~1 min 37 sec
```

The two-file probe separates fixed SSH/rclone startup cost from actual data-transfer cost, which gives a more accurate estimate than a single sample — especially for collections of many small files where handshake overhead dominates.

> **Note:** `-estimate` is skipped in `-dry-run` mode (no files are transferred to the destination).

## Summary Output

After all files are processed, a brief summary is printed to stdout:

```
--- Pre-transfer ---
  Files      : 3
  Total size : 1.5 KB
  Skipped    : 1 non-media files
  Mode       : copy to local destination (on conflict: rename)

--- Summary ---
  Media files : 3
  Success     : 2
  Failed      : 1
    no EXIF date:                  1
  Renamed     : 1 (name already taken at destination)
  Skipped     : 1 (non-media files)

--- Hints ---
  • 1 file(s) had no EXIF date. Screenshots and downloaded images usually have none —
    re-run with -use-file-modify-date to fall back to the file's modification time.
  • Renamed files kept a "_1"-style suffix. Use -on-conflict skip to leave
    duplicates in the input instead, or -on-conflict fail to stop on them.
  • Per-file errors are in sortbydate.log. Add -failure-log auto to get them as NDJSON.
```

The counts always add up to `Media files`. Lines only appear when they are non-zero:

| Line | Meaning |
|---|---|
| `Success` | Transferred |
| `Failed` | Broken down by reason underneath |
| `Renamed` | Transferred, but under a `_1`-style name |
| `Already there` | A same-sized file was already at the destination; the source was left in place |
| `Conflicts skipped` | `-on-conflict=skip` and the destination differed |
| `Not attempted` | The run ended (interrupt or circuit breaker) before these files were reached |
| `Skipped` | Not a media file — never counted in `Media files` |

`--- Hints ---` only appears when there is something actionable to say.

When `-failure-log` is set, the path is also shown in the summary:

```
  Failure log: failures-20260510-030000.ndjson
```

## Failure Log (NDJSON)

Pass `-failure-log <path>` (or `-failure-log auto` for a timestamp-based filename like `failures-20260510-030000.ndjson`) to write one JSON record per failed file. The file is **truncated** at the start of each run, so it reflects only the current run's failures.

Each line is a self-contained JSON object:

```json
{"ts":"2026-05-10T03:00:00Z","path":"/photos/IMG_1234.jpg","filename":"IMG_1234.jpg","size_bytes":3145728,"reason":"no EXIF date","error":"exiftool: no date found","duration_ms":14}
```

| Field | Type | Description |
|---|---|---|
| `ts` | string (RFC 3339) | Wall-clock time when the failure was recorded |
| `path` | string | Absolute path of the source file |
| `filename` | string | Basename of the source file |
| `size_bytes` | int64 | File size in bytes (0 if the source was already removed) |
| `reason` | string | Failure bucket (matches the summary output) |
| `error` | string | Full error message |
| `duration_ms` | int64 | Time spent processing this file, in milliseconds |

**Supported media extensions** (case-insensitive):
- Images: `.jpg` `.jpeg` `.png` `.gif` `.bmp` `.webp` `.heic` `.heif` `.tiff` `.tif` `.raw` `.cr2` `.cr3` `.nef` `.arw` `.dng` `.orf` `.rw2` `.pef` `.srw`
- Videos: `.mp4` `.mov` `.m4v` `.avi` `.mkv` `.mts` `.m2ts` `.3gp` `.3g2` `.wmv` `.flv` `.ts` `.mpg` `.mpeg`

Files with any other extension are silently skipped (logged at debug level) and appear as **Skipped** in the summary.

Possible failure reasons:
- `no EXIF date` — exiftool could not extract any date from the file
- `DateTimeOriginal not found` — file was skipped because `-only-datetimeoriginal` is set and the tag is absent
- `directory creation failed` — could not create the target `YYYY/MM` directory (local or remote)
- `file operation failed` — the move, copy, or rsync step failed

## File Metadata Preservation

When a file is **copied** (`-copy` flag, or a move that falls back to copy+delete across different filesystems), the tool restores all file-level timestamps on the destination after writing the data.

| exiftool tag | What it is | Preserved? |
|---|---|---|
| `FileModifyDate` | Modification time (`mtime`) | ✅ All platforms |
| `FileAccessDate` | Last access time (`atime`) | ✅ macOS; mtime used as fallback on Linux |
| `FileCreatedDate` | Birth time (`crtime`) | ✅ macOS (APFS/HFS+) via `setattrlist(2)` |
| `FileInodeChangeDate` | Inode change time (`ctime`) | ❌ Cannot be set — always reflects the copy time |

> **Note:** If the destination filesystem does not support writing birth time (e.g., some SMB or NFS shares), the tool logs a warning and the copy still succeeds. The `FileModifyDate` is always restored even if birth time cannot be.

**Move operations** (`os.Rename`) are atomic within the same filesystem and never alter timestamps. The copy+delete fallback (used when source and destination are on different filesystems) applies the same timestamp preservation.

## Remote Transfer Resilience (SSH Disconnect)

Three layers protect against transient or sustained SSH disconnects:

### 1. Automatic retry (`-retries`)

Passed directly to rclone as `--retries N`. On a brief SSH blip, rclone retries the transfer internally before reporting failure. Default is 3; increase for flaky links:

```bash
./build/sort_by_date -i /input -o root@nas:/photos -ssh-key ~/.ssh/id_ed25519 -retries 10
```

### 2. Partial-file cleanup

On **any** rclone failure (SSH disconnect, timeout, signal), a `rclone deletefile` is issued for the destination path using a fresh 30-second context. This ensures re-runs do not encounter stale partial files. The cleanup itself uses `--retries 1` to fail fast (if SSH is down, retrying the cleanup won't help). The local source file is always preserved — rclone only removes it after a fully successful `moveto`.

### 3. Circuit breaker (`-remote-fail-threshold`)

When consecutive remote failures reach the threshold, the circuit breaker fires:
- Cancels the run context → feeder stops dispatching new work
- Prints a clear message to stderr and the log
- Workers finish their current file, then the summary is printed

```
Circuit breaker: 5 consecutive remote failures — aborting remaining transfers
```

Use `-remote-fail-threshold 0` to disable and let all files be attempted regardless.

**Source files are never lost** regardless of which layer intervenes — the tool never deletes a local source until a remote transfer is confirmed fully successful.

## Signal Handling

The tool handles `SIGINT` (Ctrl-C), `SIGTERM`, and `SIGHUP` gracefully:

1. **Feeder stops** — no new files are dispatched to workers
2. **Workers finish current file** — in-progress operations complete their current step before exiting
3. **Queued-but-unstarted files are dropped, not attempted** — up to `-buffer` files are already sitting in the channel when the signal lands. They are counted as `Not attempted` and left exactly where they are, so Ctrl-C returns in seconds instead of grinding through a queue of transfers that would all fail anyway
4. **Recovery per operation type:**
   - `os.Rename` — atomic; no recovery needed
   - Cross-device move (copy+delete fallback): if a signal arrives after the copy but before the source delete, the delete is skipped — both source and destination exist; source is the safety net
   - `rclone moveto/copyto` — subprocess is killed immediately; a best-effort `rclone deletefile` removes any partial remote file (source is always intact — rclone never deletes local source until transfer fully succeeds)
5. **Buffers flushed** — the failure log is flushed and closed before exit
6. **Partial summary printed** — the summary includes a `WARNING: interrupted` notice and accounts for every file found

```
--- Summary ---
  WARNING: interrupted — results below are partial
  Media files : 300
  Success     : 24
  Failed      : 8
    file operation failed:         8
  Not attempted : 268 (run ended before these files were reached)
```

Exit code is `0` only when every file was handled successfully. It is `1` when the run
was interrupted or any file failed, and `2` when the command line itself was rejected
(missing `-i`/`-o`, an out-of-range `-workers`, an unknown `-on-conflict` value). This
makes the tool safe to drive from cron or a shell script.

A **second signal** (e.g. two Ctrl-C presses) reverts to default OS behaviour and terminates the process immediately.

## Logging

All operations are logged to `sortbydate.log` in the current working directory. Pass
`-log <path>` to write somewhere else — useful when the tool is run from a different
directory each time, or from a container whose working directory is a mounted volume.

The log is append-only and writes roughly one line per file, so it grows without
bound over a long-lived install. Once it passes 50 MB it is rotated to `<path>.1`
(replacing any previous rotation) at the start of the next run.