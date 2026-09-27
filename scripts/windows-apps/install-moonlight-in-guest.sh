#!/usr/bin/env bash
# Install the official Moonlight AppImage for this Omarchy user.
set -euo pipefail

if [[ $EUID == 0 ]]; then
  printf 'Run this as your normal Omarchy user, without sudo.\n' >&2
  exit 1
fi

version=6.1.0
expected_sha256=0e855ffd22d407e18ab5fdb575fed5f01ca119a3f91993c5f0213f15ac80b400
base="$HOME/.local/opt/moonlight"
appimage="$base/Moonlight-$version-x86_64.AppImage"
appdir="$base/squashfs-root"
mkdir -p -- "$base" "$HOME/.local/bin" "${XDG_DATA_HOME:-$HOME/.local/share}/applications"

if [[ ! -f $appimage ]]; then
  curl --fail --location --retry 3 \
    "https://github.com/moonlight-stream/moonlight-qt/releases/download/v$version/Moonlight-$version-x86_64.AppImage" \
    --output "$appimage.part"
  mv -- "$appimage.part" "$appimage"
fi
printf '%s  %s\n' "$expected_sha256" "$appimage" | sha256sum --check --status
chmod 0755 -- "$appimage"

# This guest does not ship the FUSE 2 library required to mount AppImages.
# Extract once and run the bundled AppRun through XWayland.
if [[ ! -x $appdir/AppRun ]]; then
  (cd -- "$base" && "$appimage" --appimage-extract >/dev/null)
fi

cat > "$HOME/.local/bin/omarchy-windows-desktop" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
export QT_QPA_PLATFORM=xcb
export DISPLAY="${DISPLAY:-:1}"
exec "$HOME/.local/opt/moonlight/squashfs-root/AppRun" "$@"
EOF
chmod 0755 -- "$HOME/.local/bin/omarchy-windows-desktop"

cat > "${XDG_DATA_HOME:-$HOME/.local/share}/applications/omarchy-windows-desktop.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Windows Desktop (Moonlight)
Comment=View and control a paired Windows Sunshine desktop
Exec=$HOME/.local/bin/omarchy-windows-desktop
Icon=$appdir/moonlight.svg
Terminal=false
Categories=Network;RemoteAccess;
EOF
printf 'Installed the Windows Desktop (Moonlight) menu entry. Pair it with Sunshine on Windows.\n'
