#!/bin/bash
set -euxo pipefail
[[ ! -e /var/lib/pacman/db.lck && ! -L /var/lib/pacman/db.lck ]]
pacman -Q try-omarchy-runtime
sudo pacman -Dk > "$HOME/package-db-before.log" 2>&1 || true
cat "$HOME/package-db-before.log"
mkdir -p "$HOME/Documents"
printf 'package recovery preservation fixture\n' > "$HOME/Documents/package-recovery.txt"
sha256sum "$HOME/Documents/package-recovery.txt" > "$HOME/package-recovery.sha256"
# Disposable Linux compatibility fixture only. The installed updater is gated;
# this explicit source-pinned upstream path exercises package/lock recovery.
GUM_CONFIRM_TIMEOUT=1s /usr/share/try-omarchy/upstream-commands/omarchy-update -y > /tmp/package-recovery-update.log 2>&1 || { cat /tmp/package-recovery-update.log; exit 1; }
cat /tmp/package-recovery-update.log
[[ ! -e /var/lib/pacman/db.lck ]]
sha256sum -c "$HOME/package-recovery.sha256"
db_status=0
sudo pacman -Dk > "$HOME/package-db-baseline.log" 2>&1 || db_status=$?
printf '%s\n' "$db_status" > "$HOME/package-db-baseline.status"
cat "$HOME/package-db-baseline.log"
