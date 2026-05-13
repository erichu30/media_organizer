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
- **Remote Sync**: Transfer files to a remote server using `rclone` (supports S3, SFTP, Dropbox, and 40+ backends). Accepts both rclone remote syntax (`remotename:path`) and SSH shorthand (`user@host:/path`).
- **Timestamp Preservation**: When copying, the destination file retains the original `FileModifyDate`, `FileAccessDate`, and (on macOS) `FileCreatedDate` — EXIF file metadata is unchanged.
- **Run Summary**: Prints a brief summary to stdout after processing — total processed, success count, and failed count broken down by reason.
- **Structured Failure Log**: Optionally writes one NDJSON record per failed file (`-failure-log auto` or `-failure-log <path>`), including path, size, reason, error, and duration.
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
  -i string
    	Input directory
  -o string
    	Output directory
  -failure-log string
    	Write failed-file records as NDJSON to this path ("auto" = timestamp-based filename)
  -only-datetimeoriginal
    	Only process files with DateTimeOriginal tag
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
	./build/sort_by_date -i /path/to/input -o /path/to/output -dry-run
	./build/sort_by_date -i /path/to/input -o /path/to/output -failure-log auto
	./build/sort_by_date -i /path/to/input -o /path/to/output -failure-log /tmp/failures.ndjson
```

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

## Logging

All operations are logged to `sortbydate.log` in the current working directory. Check this file for details when errors or unexpected behaviour occur.