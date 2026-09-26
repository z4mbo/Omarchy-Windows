#!/usr/bin/env bash
# Run as the Omarchy desktop user, from this directory in the shared folder.
set -euo pipefail

if [[ $EUID == 0 ]]; then
  printf 'Run this as your normal Omarchy user, without sudo.\n' >&2
  exit 1
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
bin_dir="$HOME/.local/bin"
applications_dir="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
mkdir -p -- "$bin_dir" "$applications_dir"
install -m 0755 -- "$script_dir/omarchy-windows-app" "$bin_dir/omarchy-windows-app"
install -m 0755 -- "$script_dir/omarchy-windows-present" "$bin_dir/omarchy-windows-present"
install -m 0755 -- "$script_dir/omarchy-windows-open" "$bin_dir/omarchy-windows-open"
install -m 0644 -- "$script_dir/omarchy_windows_protocol.py" "$bin_dir/omarchy_windows_protocol.py"
install -m 0644 -- "$script_dir/omarchy_windows_presenter_input.py" "$bin_dir/omarchy_windows_presenter_input.py"

if ! command -v sudo >/dev/null 2>&1; then
  printf 'The Windows window preview needs sudo once to install its boot-time token service.\n' >&2
  exit 1
fi
sudo install -d -m 0755 -- /usr/local/lib/try-omarchy
sudo install -m 0755 -- "$script_dir/export-seamless-token" /usr/local/lib/try-omarchy/export-seamless-token
sudo install -m 0644 -- "$script_dir/try-omarchy-seamless-token.service" /etc/systemd/system/try-omarchy-seamless-token.service
sudo systemctl daemon-reload
sudo systemctl enable --now try-omarchy-seamless-token.service

# Desktop-entry Exec accepts a quoted absolute executable path. Refuse the few
# characters which need a second layer of desktop-entry escaping.
if [[ $HOME == *'"'* || $HOME == *'\\'* || $HOME == *'$'* || $HOME == *'`'* ]]; then
  printf 'Home path needs desktop-entry escaping. Run ~/.local/bin/omarchy-windows-app manually.\n' >&2
  exit 1
fi

cat > "$applications_dir/omarchy-windows-explorer.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=File Explorer on Windows host
Comment=Open File Explorer on the Windows host
Exec="$bin_dir/omarchy-windows-app" explorer
Icon=system-file-manager
Terminal=false
Categories=System;FileManager;
EOF

cat > "$applications_dir/omarchy-windows-league.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=League on Windows host
Comment=Launch the native Windows game on the host
Exec="$bin_dir/omarchy-windows-app" league
Icon=applications-games
Terminal=false
Categories=Game;
EOF

cat > "$applications_dir/omarchy-windows-picker.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Open on Windows host (Preview)
Comment=Choose an installed Windows app to open on the host display
Exec="$bin_dir/omarchy-windows-app" pick
Icon=applications-system
Terminal=false
Categories=System;
EOF

cat > "$applications_dir/omarchy-windows-open.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Windows Apps in Omarchy (Preview)
Comment=Open a Windows app in a separate Omarchy window when capture supports it
Exec="$bin_dir/omarchy-windows-open"
Icon=video-display
Terminal=false
Categories=System;
EOF

printf 'Installed Windows host launchers and the per-window preview.\n'
