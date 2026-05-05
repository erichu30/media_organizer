# Media Organizer

A command-line tool to organize media files (photos and videos) into a directory structure based on their creation date (YYYY/MM), extracted from EXIF metadata.

## Features

- **Organize by Date**: Automatically moves or copies files into a `YYYY/MM` folder structure.
- **EXIF-based**: Extracts the creation date from EXIF metadata tags (`DateTimeOriginal`, `CreateDate`, `DateCreated`).
- **Fallback to File Date**: Can use the file's modification date if no EXIF date is found.
- **Concurrent Processing**: Uses a worker pool to process files in parallel, significantly speeding up the process for large collections.
- **Flexible Operation**: Supports both moving and copying files.
- **Dry-Run Mode**: Preview the results without making any changes to your files.
- **Remote Sync**: Transfer files to a remote server using `rsync`.
- **Logging**: Keeps a log of all operations in `sortbydate.log`.

## Dependencies

- **Go** — [golang.org](https://golang.org/)
- **ExifTool** — reads EXIF metadata; [exiftool.org](https://exiftool.org/)
- **rsync** — required only for remote transfers (`-o user@host:/path`)

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

# Remote rsync — mount your SSH key
docker run --rm \
  -v /path/to/input:/input \
  -v ~/.ssh/id_rsa:/root/.ssh/id_rsa:ro \
  media-organizer -i /input -o user@host:/remote/path
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

Organize media files by date (YYYY/MM) using EXIF data, with optional remote rsync transfer.

Required:
	-i <dir>        Input directory
	-o <dir|dest>   Output: local directory (default) OR remote destination formatted user@host:/remote/path with rsync module

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
	./build/sort_by_date -i /path/to/input -o user@host:/remote/path --copy
	./build/sort_by_date -i /path/to/input -o /path/to/output --dry-run
```

## Logging

All operations are logged to `sortbydate.log` in the current working directory. Check this file for details when errors or unexpected behaviour occur.