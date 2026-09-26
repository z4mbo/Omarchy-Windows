# Windows host integration check — 2026-09-26

Target PC: Windows 11 Pro, Core i9-14900KF (32 logical processors), 64 GiB RAM,
RTX 5080 (16 GiB), and NVMe storage. The already installed Omarchy guest lives
on the NVMe drive; its sparse Linux disk has a 200 GiB virtual capacity.

## Checked on the physical host

- A rebuilt launcher started the existing Omarchy installation in GPU mode with
  eight vCPUs and 6144 MiB guest RAM, reached the desktop, and kept the guest
  SSH and shared-folder paths usable.
- QMP reported the virtio-balloon device and fresh guest memory statistics.
  Manual and automatic requests reduced the guest's reported effective RAM,
  but QEMU's Windows working set stayed around 6.6 GiB and Windows available
  memory did not rise measurably. This QEMU build logged repeated
  `ram_block_discard_range: MADVISE not available` messages (about 89 MiB of
  logs during the controller trial). Automatic ballooning was removed from the
  launcher. Resource profiles now size the guest at boot only; no low-memory
  stress test was run during a Windows game.
- A normal Maximum Performance restart initially chose 23 vCPUs and 38144 MiB
  guest RAM. Physical QEMU working set reached 37.79 GiB and Windows free
  physical memory fell to 7.18 GiB before any Windows game started. The guest
  was shut down cleanly. The profile was changed to reserve at least one third
  of total host RAM for later Windows activity. The replacement installed
  launcher booted the same disk with 25 vCPUs and 25344 MiB guest RAM. QEMU
  working set was 25.29 GiB and Windows free physical memory was 21.2 GiB.
  The guest reached its desktop again with the window bridge token available.
- The Omarchy `omarchy-windows-app explorer` command launched File Explorer on
  the Windows host through the loopback bridge. The Start Menu catalog returned
  opaque shortcut IDs, and opening Character Map by ID launched it on Windows.
  The League shortcut exists but was not launched during an active match.
- Moonlight 6.1.0 was installed for the Omarchy user from the pinned official
  AppImage; its wrapper returned `Moonlight 6.1.0`. The Windows Desktop menu
  entry is present. Official Sunshine was installed on Windows with its service
  bound to loopback, and guest TCP access to `10.0.2.2` succeeded. Its logs
  identify the RTX 5080's NVENC H.264, HEVC and AV1 encoders. The Moonlight
  GUI ran in CPU-rendering guest mode; pairing contacted Sunshine but timed out
  without a PIN confirmation. Launching Moonlight in GPU-rendering guest mode
  repeatedly left the VM unresponsive, so a paired stream and game input were
  not established.
- A read-only call to `WTSIsChildSessionsEnabled` succeeded on this Windows 11
  Pro host and reported child sessions disabled. No child session was enabled
  or created. Whether one can isolate host-app input from the Omarchy window,
  retain GPU acceleration, or run a game remains untested.
- The authenticated per-window bridge started with a fresh QEMU `fw_cfg`
  token, and the existing personalized guest successfully exported it to a
  root:video runtime file. Both host and guest listed the same Windows windows.
  The guest fetched a 1920x1059 Notepad PNG frame. Its GTK presenter appeared
  in `hyprctl clients -j` as a non-floating client on workspace 1.
- A second presenter mirrored File Explorer. Hyprland tiled Notepad and File
  Explorer side by side as separate clients on workspace 1, and a guest
  compositor screenshot showed both applications' pixels. This validates the
  per-window preview for ordinary window capture on this host.
- The bridge accepted a posted text event for modern Notepad, but the app still
  showed zero characters. Input acceptance at the transport layer does not
  establish app control.
- A temporary Windows Forms app opened a borderless 2560x1440 host window. The
  bridge identified it as fullscreen and the guest presenter filled the
  2560x1417 Omarchy output in a compositor screenshot. The test app closed
  automatically. This is a synthetic fullscreen capture check; League match
  capture, input, latency, and client-to-match workspace transfer were not
  tested.
- The Go module passed `go test ./... -count=1`; guest shell scripts passed
  `bash -n`; and `git diff --check` found no whitespace errors.

## Limits of this check

The guest's VirGL/Venus GPU path provides graphics translation, not direct
access to the RTX 5080's CUDA or OptiX features. Native Linux game compatibility,
high-end GPU rendering, Sunshine display capture, paired input, audio and gaming
latency were not established by these checks. CPU and GPU scheduling remain with
Windows. With this QEMU build, virtio-balloon requests did not give physical RAM
back to Windows, so live RAM resizing is disabled.
