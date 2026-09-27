#!/bin/bash
set -euxo pipefail
systemctl status try-omarchy-update-repository.service --no-pager
[[ $(pacman -Q try-omarchy-runtime) == "try-omarchy-runtime $BASELINE_RUNTIME" ]]
sha256sum -c "$HOME/upgrade-preserve.sha256"
# The legacy module repair distinguishes an unowned file from a failed query.
# Check that contract against real pacman as well as the mocked shell tests.
unowned_probe=$(mktemp /tmp/try-omarchy-unowned.XXXXXX)
if ownership=$(LC_ALL=C pacman -Qqo -- "$unowned_probe" 2>&1); then
  rm -- "$unowned_probe"
  exit 1
fi
rm -- "$unowned_probe"
[[ $ownership == "error: No package owns $unowned_probe" ]]
# Prove the compatibility payload supplied loadable modules before any package
# update can mask a missing-module regression on the persistent disk.
sudo modprobe tun
[[ -c /dev/net/tun ]]
sudo modprobe v4l2loopback
[[ -c /dev/video42 ]]
[[ $(cat "/usr/lib/modules/$(uname -r)/.tryomarchy-complete") == "$(cat /usr/share/try-omarchy/compat-version)" ]]
for metadata in modules.order modules.builtin modules.builtin.modinfo; do
  [[ -f /usr/lib/modules/$(uname -r)/$metadata ]]
done
# A lock deliberately owned by this test must block the transaction unchanged.
[[ ! -e /var/lib/pacman/db.lck ]]
printf 'upgrade-test-owned-lock\n' | sudo tee /var/lib/pacman/db.lck
if sudo pacman -Syu --noconfirm > /tmp/upgrade-lock-test.log 2>&1; then exit 1; fi
grep -q 'unable to lock database' /tmp/upgrade-lock-test.log
grep -qx 'upgrade-test-owned-lock' /var/lib/pacman/db.lck
sudo rm /var/lib/pacman/db.lck
# The installed/default updater is a gate until signed package plans and
# stopped-disk rollback are implemented. Prove it does not start an upgrade.
packages_before=$(pacman -Q)
pacman_config_before=$(sha256sum /etc/pacman.conf /etc/pacman.d/mirrorlist)
if omarchy-update -y > /tmp/default-update-gate.log 2>&1; then exit 1; fi
grep -q 'No packages were changed' /tmp/default-update-gate.log
if omarchy update -y > /tmp/default-update-cli-gate.log 2>&1; then exit 1; fi
grep -q 'No packages were changed' /tmp/default-update-cli-gate.log
if omarchy-channel-set stable > /tmp/default-channel-gate.log 2>&1; then exit 1; fi
grep -q 'not changed' /tmp/default-channel-gate.log
if omarchy-update-available > /tmp/default-update-badge.log 2>&1; then exit 1; fi
[[ $(pacman -Q) == "$packages_before" ]]
[[ $(sha256sum /etc/pacman.conf /etc/pacman.d/mirrorlist) == "$pacman_config_before" ]]
# Disposable CI-only compatibility probe: invoke the source-pinned upstream
# updater by its explicit archived path on this throwaway disk. This is not a
# supported user update or evidence that the default gate has rollback.
GUM_CONFIRM_TIMEOUT=1s /usr/share/try-omarchy/upstream-commands/omarchy-update -y > /tmp/upgrade-packages.log 2>&1 || { cat /tmp/upgrade-packages.log; exit 1; }
if grep -q "command failed to execute correctly" /tmp/upgrade-packages.log; then cat /tmp/upgrade-packages.log; exit 1; fi
[[ $(pacman -Q try-omarchy-runtime) == "try-omarchy-runtime $CANDIDATE_RUNTIME" ]]
command -v zenity
pacman -Qq zenity
[[ $(cat /usr/share/omarchy/version) == "$CANDIDATE_VERSION" ]]
sha256sum -c "$HOME/upgrade-preserve.sha256"
command -v pamixer
command -v playerctl
pacman -Qq | sort > /tmp/packages-after
comm -23 <(sort "$HOME/upgrade-packages-before.txt") /tmp/packages-after > /tmp/packages-missing
[[ ! -s /tmp/packages-missing ]]
sudo pacman -Dk
sha256sum -c "$HOME/upgrade-nvim.sha256"
[[ $(readlink /usr/bin/omarchy-nvim-refresh) == "$(cat "$HOME/upgrade-nvim-link")" ]]
for helper in /usr/bin/omarchy-nvim-refresh /usr/bin/omarchy-nvim-setup; do
  [[ $(pacman -Qqo "$helper") == omarchy-nvim ]]
done
sudo pacman -Qk try-omarchy-runtime
# Revision 22 puts the packaged Neovim theme link back on existing disks and
# catch-up restores it for the instant account created from the broken skeleton.
sudo pacman -Qk omarchy-nvim
expected_theme_link="../../../../.local/state/omarchy/current/theme/neovim.lua"
[[ $(readlink /etc/skel/.config/nvim/lua/plugins/theme.lua) == "$expected_theme_link" ]]
[[ $(readlink "$HOME/.config/nvim/lua/plugins/theme.lua") == "$expected_theme_link" ]]
omarchy-migrate
sha256sum -c "$HOME/upgrade-preserve.sha256"
sync
