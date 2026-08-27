---
name: devops
description: Build, Docker, and rclone configuration for media_organizer — multi-stage image, SSH on-the-fly syntax, runtime dependencies.
skills:
  - fullstack-dev-skills:devops-engineer
---

# DevOps / Infrastructure

## When to Invoke

| Skill | Invoke When |
|---|---|
| `/devops-engineer` | Dockerfile changes, build.sh, CI/CD pipelines, rclone configuration |

## Project Context

**Build** (`build.sh`)
```bash
go build -o ./build/sort_by_date ./src/cmd/
```
Cross-compile for Linux: set `GOOS=linux GOARCH=amd64` before running.

**Docker** (`Dockerfile`)
- Multi-stage build: Go builder + runtime image
- Runtime dependencies that must be present in the final image: `exiftool`, `rclone`
- No `rsync` — rclone handles all remote transfers

**rclone SSH on-the-fly syntax** (auto-converted in `NewConfig`)
```
user@host:/path  →  :sftp,host=<host>,user=<user>[,key_file=<key>]:<path>
```
Configured remotes (`remotename:/path`) pass through unchanged.

**Runtime dependencies**
| Binary | Required | Purpose |
|---|---|---|
| `exiftool` | Always | Read EXIF metadata |
| `rclone` | Remote mode only | Transfer files, create remote dirs |

**rclone flags used**
- `moveto` / `copyto` — per-file transfers (current)
- `--retries N` — passed through from `-retries` flag
- `--log-level DEBUG` — used in cleanup to detect partial files
- `deletefile --retries 1` — cleanup after any rclone failure

## Key Files

- `Dockerfile` — multi-stage build
- `build.sh` — native build script
- `src/cmd/config.go` — `toRcloneSFTPPath`, SSH conversion
- `src/cmd/fileops.go` — `transferRemote`, `cleanupRemoteFile`
