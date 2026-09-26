#!/bin/bash
# This disk was restored in full from the stopped pre-cut backup.
set -euxo pipefail
source "$(dirname "$0")/common.sh"

[[ ! -e /var/lib/pacman/db.lck && ! -L /var/lib/pacman/db.lck ]]
check_version_one
check_preserved
pacman -Qq | sort > "$work/packages.current"
cmp "$work/packages.before" "$work/packages.current"
check_baseline_db

# A restored disk must remain usable for a normal package upgrade.
sudo pacman -U --noconfirm "$work/fixture-2.0-1.pkg.tar.zst"
check_version_two
check_preserved
check_baseline_db
[[ ! -e /var/lib/pacman/db.lck ]]
sync
