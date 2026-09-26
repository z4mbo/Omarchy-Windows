#!/bin/bash
set -euxo pipefail
source "$(dirname "$0")/common.sh"

[[ ! -e /var/lib/pacman/db.lck && ! -L /var/lib/pacman/db.lck ]]
check_version_two
check_preserved
check_baseline_db
pacman -Qq | sort > "$work/packages.current"
cmp "$work/packages.before" "$work/packages.current"
sync
