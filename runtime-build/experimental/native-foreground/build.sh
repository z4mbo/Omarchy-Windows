#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 OUTPUT_DIRECTORY" >&2
    exit 2
fi

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
output=$(mkdir -p "$1" && cd "$1" && pwd)
work=$(mktemp -d)
trap 'chmod -R u+w "$work" 2>/dev/null || true; rm -rf -- "$work"' EXIT

python "$here/prepare_recipe.py" "$work/runtime-build"
python "$work/runtime-build/validate-lock.py" "$work/runtime-build/sources.lock.json"
bash "$work/runtime-build/build.sh" "$output"
