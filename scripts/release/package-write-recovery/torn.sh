#!/bin/bash
# Diagnose the crashed child disk without mutating its pre-cut backup.
set -euxo pipefail
source "$(dirname "$0")/common.sh"

size=$(stat -c %s "$installed_payload")
[[ $size -gt 0 && $size -le 4096 && $size -lt $payload_bytes ]]
cmp -n "$size" "$work/payload-v2.bin" "$installed_payload"
pacman -Q "$package_name" > "$work/package-query-torn.log" 2>&1 || true
cat "$work/package-query-torn.log"
if check_preserved; then
  echo 'TORN_PRESERVED:yes'
else
  echo 'TORN_PRESERVED:no'
fi
sudo journalctl -b -u try-omarchy-pacman-lock.service --no-pager || true
status=0
sudo pacman -Dk > "$work/package-db-torn.log" 2>&1 || status=$?
printf 'TORN_DB_STATUS:%s\n' "$status"
cat "$work/package-db-torn.log"
printf 'TORN_PAYLOAD_BYTES:%s\n' "$size"
sync
