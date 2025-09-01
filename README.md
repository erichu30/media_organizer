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

Before using this tool, you need to have the following software installed:

- **Go**: The programming language used to build the tool. You can install it from the [official Go website](https://golang.org/).
- **ExifTool**: A command-line tool for reading and writing EXIF data. You can install it from the [official ExifTool website](https://exiftool.org/).
- **rsync**: A command-line tool for transferring files. It is used for the remote sync feature.

## Installation

1.  **Clone the repository:**
    ```bash
    git clone <repository-url>
    cd media_organizer
    ```

2.  **Build the tool:**
    A build script is provided for convenience.
    ```bash
    ./build.sh
    ```
    This will create an executable named `sort_by_date` in the `build` directory.

## Usage

```
Usage: ./build/sort_by_date [OPTIONS]

Organize media files by date (YYYY/MM) using EXIF data, with optional remote rsync transfer.

Required:
	-i <dir>        Input directory
	-o <dir|dest>   Output: local directory (default) OR remote rsync destination when used with --rsync

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

The tool logs all its operations to a file named `sortbydate.log` in the same directory where you run the tool. In case of errors or unexpected behavior, this file will contain detailed information.