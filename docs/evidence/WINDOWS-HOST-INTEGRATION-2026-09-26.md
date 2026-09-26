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
- The idle League client exposed a 1280x720 capturable frame and appeared as
  its own non-floating Hyprland client on workspace 1. The preview process was
  stopped without closing the native Windows League client. No League input or
  live match was tested.
- After the host-grant bridge update, an authenticated catalogue initially
  returned no host windows, and an unauthenticated request returned HTTP 401.
  Launching Character Map through the guest preview command granted only its
  new `charmap.exe` window. Its presenter appeared as a non-floating Hyprland
  client on workspace 1. Closing that window through the authenticated bridge
  removed it from the catalogue. Existing-window tray grant/revoke UI was not
  exercised in this host test.
- The bridge accepted a posted text event for modern Notepad, but the app still
  showed zero characters. Input acceptance at the transport layer does not
  establish app control.
- A controlled native Win32 Edit test delivered text to a child control
  without changing the Windows foreground app. This verifies the new routing
  for that control type; modern Notepad and game input remain unverified.
- A live Computer Use pass opened Character Map from the guest and showed it
  as a separate tiled Hyprland window. The Select button accepted a mouse
  click, but selecting the visually indicated B cell repeatedly appended an
  exclamation mark in the copied-characters field. Windows accessibility
  reported `Selected Character: U+0021: Exclamation Mark` and field value
  `!!` after two attempts. This remains a failed guest GTK presenter test;
  later isolated checks below narrowed the cause but did not repeat the
  visible in-Omarchy click.
- With the VM temporarily in a 722×432 host window, `hyprctl monitors -j`
  reported a 720×400 guest mode at about 360 Hz. When the VM was maximized,
  the guest had previously reported 2560×1417 at 360 Hz, matching the host
  work area height rather than its full 2560×1440 panel. This prompted the
  full-display install default and refresh-rate work.
- The rebuilt launcher was installed over the existing launcher after a clean
  guest shutdown, with the personalized disk retained. Its QEMU command line
  included `video=2560x1440`, `-full-screen`, and the bundled runtime's
  `refresh-rate=360000` option. The running guest then reported
  `Virtual-1` at 2560×1440 and 360.039 Hz through `hyprctl monitors -j`;
  the Windows primary monitor reported 2560×1440 at 360 Hz. Computer Use
  showed the Omarchy desktop filling the monitor. This verifies the primary
  monitor on this installed PC; mixed-refresh secondary outputs remain a
  documented limitation.
- With opt-in Windows Graphics Capture enabled, a guest launch granted a new
  Character Map window. The first authenticated frame request timed out at
  six seconds while the helper was starting; a later request returned a
  487×435 PNG in under a second. After the launcher prewarm change, a cold
  reboot and fresh Character Map launch returned the first frame in 4.2
  seconds through `PrintWindow` fallback (491×437), then a 487×435 WGC
  frame in 0.44 seconds. The presenter appeared as a non-floating Hyprland
  client on workspace 1. The guest was locked during screenshot inspection,
  so this test verifies frame transport and window creation, not a visual
  usability or input pass.
- A later isolated input check used a second bridge on host loopback port
  4458, with only an already open Character Map window granted. The installed
  launcher, its port 4457 bridge, and the guest disk image were not replaced.
  The revised host code padded a 487×435 Windows Graphics Capture frame to
  the full 491×437 window rectangle. The guest fetched that 491×437 PNG over
  its normal QEMU host network route and posted a B-cell click at window-local
  (316,117); Windows accessibility changed from `U+0021: Exclamation Mark`
  to `U+0042: Latin Capital Letter B`. A direct host bridge input test made
  the same selection. This verifies capture alignment, authenticated network
  transport, and posted input for this Win32 grid control. The guest GTK
  presenter itself was not clicked in this pass because Omarchy had entered
  its password lock screen. The earlier visual mismatch is therefore still
  unresolved. Modern Notepad text input and games remain unverified.
- A later attempt to repeat the visible GTK presenter click encountered the
  Omarchy screensaver and then a user-stopped Computer Use session. The test
  made no visual click or password entry. The isolated bridge B selection
  above remains the latest positive input result; the in-Omarchy presenter
  click has not passed.
- Two initial direct WGC probes of PowerShell-launched Character Map windows
  timed out. A Character Map window launched through Computer Use yielded a
  487×435 WGC frame in under one second, including while occluded by QEMU.
  The capture timeout was not reproduced with that interactive launch; the
  reason for the earlier window-specific delay was not established.
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
