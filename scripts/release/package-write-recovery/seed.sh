#!/bin/bash
# Seed a completely disposable VM disk before the offline backup is taken.
set -euxo pipefail
source "$(dirname "$0")/common.sh"

[[ ! -e $work && ! -e $installed_payload ]]
[[ ! -e /var/lib/pacman/db.lck && ! -L /var/lib/pacman/db.lck ]]
mkdir -p "$work" "$HOME/Documents"

printf 'keep this personal document through package rollback\n' > "$HOME/Documents/package-write-preserve.txt"
printf 'user-customized=true\n' | sudo tee /etc/try-omarchy-package-write-test.conf
(cd / && sha256sum "${HOME#/}/Documents/package-write-preserve.txt" \
  etc/try-omarchy-package-write-test.conf) > "$work/preserved.sha256"

python3 - "$work/payload-v1.bin" "$work/payload-v2.bin" <<'PY'
import hashlib
from pathlib import Path
import sys

for version, name in enumerate(sys.argv[1:], 1):
    block = hashlib.sha256(f"Try Omarchy interrupted package payload v{version}".encode()).digest() * 128
    assert len(block) == 4096
    with Path(name).open("wb") as output:
        for _ in range(2048):
            output.write(block)
PY
for version in 1 2; do
  [[ $(stat -c %s "$work/payload-v$version.bin") == "$payload_bytes" ]]
  sha256sum "$work/payload-v$version.bin" > "$work/payload-v$version.sha256"
done

make_fixture() {
  local version=$1 text=$2 tree="$work/tree-$1" archive="$work/fixture-$1.pkg.tar.zst"
  mkdir -p "$tree/usr/share/$package_name"
  printf '%s\n' "$text" > "$tree/usr/share/$package_name/version.txt"
  local payload_version=1
  if [[ $version == 2.0-1 ]]; then
    payload_version=2
  fi
  cp "$work/payload-v$payload_version.bin" "$tree/usr/share/$package_name/payload.bin"
  cat > "$tree/.PKGINFO" <<PKG
pkgname = $package_name
pkgbase = $package_name
pkgver = $version
pkgdesc = Disposable partial package write fixture
url = https://github.com/omacom/try-omarchy-windows
builddate = 1789344000
packager = Try Omarchy recovery test
size = $(du -sb "$tree/usr" | cut -f1)
arch = any
license = MIT
PKG
  local -a entries=(.PKGINFO usr usr/share "usr/share/$package_name")
  entries+=("usr/share/$package_name/payload.bin")
  entries+=("usr/share/$package_name/version.txt")
  (cd "$tree" && tar --owner=0 --group=0 --no-recursion -cf - "${entries[@]}" | zstd -q -o "$archive")
  [[ -s $archive ]]
}

make_fixture 1.0-1 version-one
make_fixture 2.0-1 version-two
sudo pacman -U --noconfirm "$work/fixture-1.0-1.pkg.tar.zst"
pacman -Q pacman libarchive
pacman --version
check_version_one
check_preserved
pacman -Qq | sort > "$work/packages.before"
status=0
sudo pacman -Dk > "$work/package-db-before.log" 2>&1 || status=$?
[[ $status -eq 0 ]]
printf '%s\n' "$status" > "$work/package-db-before.status"
cat "$work/package-db-before.log"
sync
