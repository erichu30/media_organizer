---
name: documentation
description: Documentation guidance for media_organizer — README updates, godoc comments, usage examples, and keeping reference docs in sync.
skills:
  - fullstack-dev-skills:code-documenter
---

# Documentation

## When to Invoke

| Skill | Invoke When |
|---|---|
| `/code-documenter` | Updating README.md, adding godoc comments, generating usage examples |

## Doc Map

| File | Owner | Update When |
|---|---|---|
| `README.md` | User-facing | New flags, changed behaviour, new runtime deps |
| `CLAUDE.md` | Claude-facing | Commands change, new reference docs added |
| `ARCHITECTURE.md` | Design | Package structure changes, new design decisions |
| `ERRORS.md` | Pitfalls | New gotcha discovered during development |
| `CONVENTIONS.md` | Style | New pattern established, existing pattern changed |
| `TASKS.md` | Planned work | Task completed or new task added |
| `docs/` | Performance | Benchmarks re-run, memory analysis updated |

## Godoc Style for This Project

- No comments on obvious functions — `isMediaFile`, `expandTilde` need no doc
- Comment when the WHY is non-obvious: hidden invariant, platform difference, workaround
- `ExifService` interface: document each method's contract (what the `tag` return string contains)
- Do not reference issue numbers or PR links in comments — put them in commit messages

## README Sections to Keep Current

1. **Usage** — all CLI flags with examples (especially `-ssh-key`, `-failure-log auto`, `-estimate`)
2. **Requirements** — `exiftool` and `rclone` version constraints
3. **Docker** — `docker run` example with correct volume mounts
4. **Output format** — the `YYYY/MM/filename` destination structure
