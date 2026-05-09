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
- **Remote Sync**: Transfer files to a remote server using `rsync`.
- **Run Summary**: Prints a brief summary to stdout after processing — total processed, success count, and failed count broken down by reason.
- **Logging**: Keeps a log of all operations in `sortbydate.log`.

## Dependencies

- **Go** — [golang.org](https://golang.org/)
- **ExifTool** — reads EXIF metadata; [exiftool.org](https://exiftool.org/)
- **rclone** — required only for remote transfers (`-o remotename:path`); supports S3, GCS, Dropbox, SFTP, and 40+ other backends

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

Organize media files by date (YYYY/MM) using EXIF data, with optional remote rclone transfer.

Required:
	-i <dir>        Input directory
	-o <dir|dest>   Output: local directory (default) OR rclone remote destination formatted as remotename:path

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
  -only-datetimeoriginal
    	Only process files with DateTimeOriginal tag
  -use-file-modify-date
    	Use file modify date as a fallback
  -workers int
    	Number of concurrent workers (default 8)

Examples:
	./build/sort_by_date -i /path/to/input -o /path/to/output
	./build/sort_by_date -i /path/to/input -o myremote:/photos --copy
	./build/sort_by_date -i /path/to/input -o /path/to/output --dry-run
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

**Supported media extensions** (case-insensitive):
- Images: `.jpg` `.jpeg` `.png` `.gif` `.bmp` `.webp` `.heic` `.heif` `.tiff` `.tif` `.raw` `.cr2` `.cr3` `.nef` `.arw` `.dng` `.orf` `.rw2` `.pef` `.srw`
- Videos: `.mp4` `.mov` `.m4v` `.avi` `.mkv` `.mts` `.m2ts` `.3gp` `.3g2` `.wmv` `.flv` `.ts` `.mpg` `.mpeg`

Files with any other extension are silently skipped (logged at debug level) and appear as **Skipped** in the summary.

Possible failure reasons:
- `no EXIF date` — exiftool could not extract any date from the file
- `DateTimeOriginal not found` — file was skipped because `-only-datetimeoriginal` is set and the tag is absent
- `directory creation failed` — could not create the target `YYYY/MM` directory (local or remote)
- `file operation failed` — the move, copy, or rsync step failed

## Logging

All operations are logged to `sortbydate.log` in the current working directory. Check this file for details when errors or unexpected behaviour occur.