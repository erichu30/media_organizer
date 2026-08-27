#!/usr/bin/env bash
# Runs gofmt -w on any .go file after it is written or edited.
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

gofmt -w "$file_path"
exit 0
