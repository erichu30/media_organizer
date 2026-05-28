#!/usr/bin/env bash
# Blocks file edits when on the main branch to prevent accidental direct commits.
set -euo pipefail

branch=$(git branch --show-current 2>/dev/null || echo "")

if [ "$branch" = "main" ]; then
  echo "Blocked: direct edits on 'main' are not allowed." >&2
  echo "Create a feature branch first: git checkout -b feat/<name>" >&2
  exit 2
fi

exit 0
