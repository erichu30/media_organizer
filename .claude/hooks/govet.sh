#!/usr/bin/env bash
# Runs go vet on the package containing the edited .go file.
# Non-fatal: output is shown but the edit is not blocked.
set -euo pipefail

input=$(cat)
file_path=$(echo "$input" | python3 -c "
import sys, json
d = json.load(sys.stdin)
print(d.get('tool_input', {}).get('file_path', ''))
" 2>/dev/null || echo "")

if [[ -z "$file_path" || "$file_path" != *.go ]]; then
  exit 0
fi

if [ ! -f "$file_path" ]; then
  exit 0
fi

repo_root=$(git rev-parse --show-toplevel 2>/dev/null || echo "")
if [ -z "$repo_root" ]; then
  exit 0
fi

dir=$(dirname "$file_path")
rel=$(python3 -c "import os; print(os.path.relpath('$dir', '$repo_root'))" 2>/dev/null || echo "")

if [ -z "$rel" ]; then
  exit 0
fi

cd "$repo_root"
go vet "./$rel" 2>&1 || true  # non-fatal; show output but never block
exit 0
