#!/bin/bash
set -euo pipefail

readonly package_name=try-omarchy-package-write-test
readonly work="$HOME/package-write-recovery"
readonly installed_dir=/usr/share/try-omarchy-package-write-test
readonly installed_payload="$installed_dir/payload.bin"
readonly payload_bytes=$((8 * 1024 * 1024))

check_preserved() {
  (cd / && sha256sum -c "$work/preserved.sha256")
}

check_baseline_db() {
  local status=0
  sudo pacman -Dk > "$work/package-db-current.log" 2>&1 || status=$?
  [[ $status -eq 0 ]]
  [[ $status == "$(cat "$work/package-db-before.status")" ]]
  cmp "$work/package-db-before.log" "$work/package-db-current.log"
}

check_version_one() {
  [[ $(pacman -Q "$package_name") == "$package_name 1.0-1" ]]
  [[ $(cat "$installed_dir/version.txt") == version-one ]]
  [[ $(stat -c %s "$installed_payload") == "$payload_bytes" ]]
  sha256sum -c "$work/payload-v1.sha256"
  cmp "$work/payload-v1.bin" "$installed_payload"
  sudo pacman -Qk "$package_name"
}

check_version_two() {
  [[ $(pacman -Q "$package_name") == "$package_name 2.0-1" ]]
  [[ $(cat "$installed_dir/version.txt") == version-two ]]
  [[ $(stat -c %s "$installed_payload") == "$payload_bytes" ]]
  sha256sum -c "$work/payload-v2.sha256"
  cmp "$work/payload-v2.bin" "$installed_payload"
  sudo pacman -Qk "$package_name"
}
