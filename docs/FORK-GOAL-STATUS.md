# Requested Omarchy for Windows product

Updated September 27, 2026. Development remains on `codex/native-windows-apps`
in draft [PR #1](https://github.com/z4mbo/Omarchy-Windows/pull/1). The goal is
**not complete** and no complete installer has been released.

## Accepted requirements

The user restated the target as: **run Omarchy inside Windows with maximum
performance like a native install, and run native Windows apps inside the
Omarchy experience at full speed and quality**. Recreating the VM or using
different technologies is explicitly allowed.

The user then explicitly accepted **Windows drawing each app directly in its
assigned Omarchy tile**, while Omarchy controls position, workspace, and
fullscreen behavior. A window remaining technically on the Windows side is
acceptable. This opens a direct-presentation architecture; the earlier
capture-only design is no longer the required implementation.

- Windows 11 remains the base operating system.
- One app named **Omarchy** installs and manages the full experience.
- Linux applications use the CPU, GPU, RAM, and SSD with native-like capability;
  resources adapt to competing Windows activity while Omarchy runs.
- Game development, games, 3D modeling, and rendering work in Omarchy.
- Windows applications appear individually in Hyprland, with tiling, workspaces,
  normal input, and fullscreen games in the launching workspace.
- Windows apps keep their native rendering and input path while appearing in
  Omarchy's layout. Direct Windows presentation is accepted; pixel capture
  into Linux is not required.
- Resolution and refresh rate follow the Windows monitor automatically.
- Automatic guest locking is disabled through Omarchy's persistent Stay Awake
  preference. New installations start with that preference; later user choices
  must survive updates.
- Future Omarchy updates are part of the product's acceptance requirements.
  Host/guest integration must preserve compatibility, user files, and settings
  across supported upgrades. New upstream releases need upgrade and rollback
  validation before being offered automatically; an unverified release must not
  silently replace the working system. Unknown future releases cannot be
  certified in advance, so the product needs continuing compatibility checks
  and a recoverable update path.
- Replace the old installed app and settings with the completed version, then
  update this repository's README.

## Current architecture decision

Keep the working QEMU Hyprland desktop for now. Prototype a guest layout proxy
for each granted Windows window and a host controller that places the real
Windows window in the corresponding tile. Only layout and lifecycle metadata
cross the boundary. Windows renders the app and receives its ordinary input;
the native mode must not poll frames or relay application keystrokes.

The first prototype is opt-in and limited to one output. Character Map has
passed a [physical native input, tiling and workspace test](evidence/NATIVE-DESKTOP-2026-09-27.md).
Fullscreen, broader app coverage, focus, popup ownership, DPI and reliable
restoration still need validation before installation or default use. Native rendering removes the existing PNG-stream bottleneck, but does not
prove that all apps behave correctly or that performance equals bare metal.

WSL remains the measured candidate for Linux GPU compute and selected graphics
workloads. Existing Waypipe/CUDA evidence is insufficient to claim an
interactive, fully accelerated Linux GPU desktop. See the
[direct-presentation plan](NATIVE-WINDOW-PRESENTATION.md).

## Why the prior capture-only design was blocked

The installed capture architecture runs Windows applications on the host and presents
their captured windows in Linux. It cannot meet the universal requirement.

Windows applications can request exclusion from supported capture APIs through
[`SetWindowDisplayAffinity`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowdisplayaffinity).
This is an API capture restriction, not a claim that no physical recording is
possible. The [desktop duplication API](https://learn.microsoft.com/en-us/windows-hardware/drivers/display/desktop-duplication-api)
also protects protected video content.

UAC and sign-in use the [Winlogon desktop](https://learn.microsoft.com/en-us/windows/win32/winstation/desktops),
whose access is restricted. The ordinary host bridge cannot treat those surfaces
as ordinary application windows. Privileged remoting can support some secure
desktop transitions, but does not establish the requested universal per-window
Hyprland behavior. No implementation demonstrating that guarantee is available.

The later acceptance of direct native Windows presentation changes this design
constraint. The project must preserve that distinction and must not describe a
host-rendered window as pixels rendered by the Linux compositor. Whole-desktop
streaming remains different from the requested individual-window experience.

## Work preserved

- A branded launcher and integrated settings are implemented; the latest tested
  launcher was installed while preserving the existing personalized Linux disk.
- Individual Windows-window presentation is a preview with known input failures
  and no verified League gameplay. The full preview is not game-ready.
- Primary-monitor 2560×1440 at approximately 360 Hz was observed in the existing
  guest; all-monitor and live-change guarantees remain unproven.
- Automatic profiles select resources at launch and now adjust QEMU CPU
  scheduling priority under sustained background host load. Live vCPU count and
  RAM changes are not enabled in normal launches.
- The [private Windows RAM adapter](evidence/BALLOON-ADAPTER-2026-09-26.md)
  passed a no-pressure test alongside three independent VM control connections.
  [WSL app forwarding](evidence/WSL-WAYPIPE-COMPANION-2026-09-26.md) registered
  Blender as a tiled guest window while a separate WSL process rendered with
  CUDA. These are isolated experiments, not bundled capabilities of the app.
- Fresh portable and standard setup functions passed isolated tests. A full
  graphical one-click installation and signed release remain open. Repository
  release signing credentials are not configured.

The README and linked evidence distinguish observed results from unfinished
work. The old personalized installation has not been removed and replaced with
a purportedly complete product, because the requested replacement does not
exist. The current preview must not be reported as fulfilling this goal.
