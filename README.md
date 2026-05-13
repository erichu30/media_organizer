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
- **Run Summary**: Prints a brief summary to stdout after processing — total processed, success count, and failed count broken down by reason.
- **Structured Failure Log**: Optionally writes one NDJSON record per failed file (`-failure-log auto` or `-failure-log <path>`), including path, size, reason, error, and duration.
- **Pre-transfer Info**: Before starting, prints file count and total size. With `-estimate` (remote mode), transfers two sample files (10th and 90th percentile by size) to measure per-file SSH overhead and bandwidth, then shows a projected total transfer time.
- **Signal Handling**: Responds to `SIGINT` (Ctrl-C), `SIGTERM`, and `SIGHUP` with a graceful shutdown — in-progress file operations finish before exit, all buffers are flushed, and the summary is printed with a partial-results warning. A second signal force-quits immediately.
- **Logging**: Keeps a log of all operations in `sortbydate.log`.

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
  -i string
    	Input directory
  -o string
    	Output directory
  -failure-log string
    	Write failed-file records as NDJSON to this path ("auto" = timestamp-based filename)
  -only-datetimeoriginal
    	Only process files with DateTimeOriginal tag
  -remote-fail-threshold int
    	abort after this many consecutive remote failures; 0 to disable (default 5)
  -retries int
    	rclone retry count for transient remote errors — SSH disconnect, timeouts (default 3)
  -ssh-key string
    	Path to SSH private key for user@host:/path destinations (e.g. ~/.ssh/id_ed25519)
  -use-file-modify-date
    	Use file modify date as a fallback
  -workers int
    	Number of concurrent workers (default 8)

Examples:
	./build/sort_by_date -i /path/to/input -o /path/to/output
	./build/sort_by_date -i /path/to/input -o myremote:/photos -copy
	./build/sort_by_date -i /path/to/input -o root@192.168.1.10:/mnt/nas/photos -ssh-key ~/.ssh/id_ed25519
	./build/sort_by_date -i /path/to/input -o root@192.168.1.10:/mnt/nas/photos -ssh-key ~/.ssh/id_ed25519 -estimate
	./build/sort_by_date -i /path/to/input -o /path/to/output -dry-run
	./build/sort_by_date -i /path/to/input -o /path/to/output -failure-log auto
	./build/sort_by_date -i /path/to/input -o /path/to/output -failure-log /tmp/failures.ndjson
```

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
--- Summary ---
  Processed : 150
  Success   : 143
  Failed    : 7
    DateTimeOriginal not found:      3
    directory creation failed:       1
    file operation failed:           2
    no EXIF date:                    1
```

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
3. **Recovery per operation type:**
   - `os.Rename` — atomic; no recovery needed
   - Cross-device move (copy+delete fallback): if a signal arrives after the copy but before the source delete, the delete is skipped — both source and destination exist; source is the safety net
   - `rclone moveto/copyto` — subprocess is killed immediately; a best-effort `rclone deletefile` removes any partial remote file (source is always intact — rclone never deletes local source until transfer fully succeeds)
4. **Buffers flushed** — the failure log is flushed and closed before exit
5. **Partial summary printed** — the summary includes a `WARNING: interrupted` notice

```
--- Summary ---
  WARNING: interrupted — results below are partial
  Processed : 142
  Success   : 87
  Failed    : 2
    no EXIF date:                    2
```

Exit code is `1` when interrupted, `0` on a complete run.

A **second signal** (e.g. two Ctrl-C presses) reverts to default OS behaviour and terminates the process immediately.

## Logging

All operations are logged to `sortbydate.log` in the current working directory. Check this file for details when errors or unexpected behaviour occur.