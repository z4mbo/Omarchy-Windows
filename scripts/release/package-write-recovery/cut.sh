#!/bin/bash
# Hard-power-cut the disposable VM only after a real pacman payload write.
set -euxo pipefail
source "$(dirname "$0")/common.sh"

[[ ${PACKAGE_WRITE_NONCE:-} =~ ^[0-9a-f]{32}$ ]]
[[ ${PACKAGE_WRITE_PRELOAD:-} == /mnt/preload/pause-write.so ]]
[[ ! -e /run/try-omarchy-package-cut.ready ]]
[[ ! -e /var/lib/pacman/db.lck ]]
check_version_one
check_preserved

sudo mkdir -p /mnt/preload
sudo mount -t 9p -o ro,trans=virtio preload /mnt/preload
sudo test -s "$PACKAGE_WRITE_PRELOAD"
sudo cp "$PACKAGE_WRITE_PRELOAD" "$work/pause-write.so"
sudo chmod 0644 "$work/pause-write.so"

unit=try-omarchy-package-write-cut.service
sudo systemd-run --unit="$unit" --property=Type=exec \
  --setenv="LD_PRELOAD=$work/pause-write.so" \
  --setenv="TRY_OMARCHY_CUT_TARGET=$installed_payload" \
  --setenv="TRY_OMARCHY_CUT_NONCE=$PACKAGE_WRITE_NONCE" \
  /usr/bin/pacman -U --noconfirm "$work/fixture-2.0-1.pkg.tar.zst"

pid=0
for attempt in $(seq 1 120); do
  pid=$(sudo systemctl show --property=MainPID --value "$unit")
  if [[ -f /run/try-omarchy-package-cut.ready && $pid =~ ^[1-9][0-9]*$ && $pid -gt 1 ]] &&
     sudo grep -Eq '^State:[[:space:]]+T' "/proc/$pid/status"; then
    break
  fi
  sleep 1
done
[[ -f /run/try-omarchy-package-cut.ready ]]
grep -Eq "^$PACKAGE_WRITE_NONCE:[1-9][0-9]*$" /run/try-omarchy-package-cut.ready
written=$(cut -d: -f2 /run/try-omarchy-package-cut.ready)
[[ $pid =~ ^[1-9][0-9]*$ && $pid -gt 1 ]]
[[ $(sudo readlink "/proc/$pid/exe") == /usr/bin/pacman ]]
sudo grep -Eq '^State:[[:space:]]+T' "/proc/$pid/status"
[[ -f /var/lib/pacman/db.lck && ! -L /var/lib/pacman/db.lck ]]
if sudo pacman -U --noconfirm "$work/fixture-1.0-1.pkg.tar.zst" > "$work/busy.log" 2>&1; then
  echo 'Competing pacman transaction unexpectedly acquired the lock' >&2
  exit 1
fi
grep -q 'unable to lock database' "$work/busy.log"
size=$(stat -c %s "$installed_payload")
[[ $size -eq $written && $size -gt 0 && $size -le 4096 && $size -lt $payload_bytes ]]
cmp -n "$size" "$work/payload-v2.bin" "$installed_payload"
pacman -Q "$package_name" > "$work/package-query-at-cut.log" 2>&1 || true
cat "$work/package-query-at-cut.log"
check_preserved
sync
printf 'PACKAGE_WRITE_CUT_READY:%s\n' "$PACKAGE_WRITE_NONCE"
# The harness SIGKILLs only its owned QEMU process after seeing the exact line.
while :; do sleep 60; done
