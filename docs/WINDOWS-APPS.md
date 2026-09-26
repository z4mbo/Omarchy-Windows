# Windows apps in Omarchy

The intended experience is one Omarchy app on Windows, with each selected
Windows application appearing as its own Hyprland window in an Omarchy
workspace. A game that opens a separate fullscreen window should occupy the
same workspace as its launcher. This is an **experimental target**, not a
completed gaming feature.

## Per-window preview

**Windows Apps in Omarchy (Preview)** opens a Windows Start Menu app through
the Windows host, then creates a separate GTK4 window inside Omarchy for the
selected Windows window. Hyprland can tile and move that GTK window like other
Omarchy apps. The Windows launcher grants access only to a new window tied to
the freshly launched process. Existing windows, including those reused by
single-instance apps, require a choice from the Windows tray menu: **Show
Windows app in Omarchy...** Select the specific window there. Use **Stop
showing Windows app** in the tray to revoke it. Modern Notepad and File
Explorer may need this one-click choice. Unrelated host windows stay private.
The app itself still runs on Windows with its installed driver and files.

The Windows launcher serves an authenticated, loopback-only window bridge at
`10.0.2.2:4457` from the guest. A fresh bearer token is passed to that guest
boot through QEMU `fw_cfg`; the token is not placed in the command line. The
bridge lists only host-granted windows with opaque IDs, captures individual
ordinary windows as PNG images, and accepts bounded mouse, keyboard, and close
events for those granted windows.
It does not expose a command or script execution endpoint. The guest presenter
uses a separate GTK window per Windows window and limits pending frames.

On the September 26 Windows 11 test host, Notepad and File Explorer both
rendered in separate non-floating Hyprland windows and tiled side by side on
workspace 1. The guest received their frames through the authenticated bridge.
The idle League client also rendered as a separate guest window; match capture
and input remain untested. Modern Notepad ignored a posted text event, so this
preview is not yet a dependable way to control every Windows app. In a live
Character Map test, clicking the visible B cell inside Omarchy left Windows'
selected character at U+0021 (exclamation mark); pressing Select appended
that wrong character. See the
[host test notes](evidence/WINDOWS-HOST-INTEGRATION-2026-09-26.md).

The default capture uses Windows `PrintWindow` and a PNG-over-HTTP transport.
An opt-in Windows Graphics Capture prototype (`OMARCHY_SEAMLESS_WGC=1`) captured
one Character Map window through the guest bridge on the test host. It starts
the helper in the background and falls back to `PrintWindow` while capture
warms up or fails. The PNG transport remains preview speed and has
not been tested with a live game. Some apps ignore posted input, and the bridge does not
move focus to a host window or resize it: the host app and Omarchy share one
Windows desktop, so either action can cover or unfocus Omarchy. A Windows
input-isolation and game transport path needs further work and physical
validation.

The presenter records an app group's Hyprland workspace. When a new fullscreen
window from that group appears, it attempts to place the new GTK window on
that workspace and asks Hyprland to fullscreen it. This is the intended
League-client-to-match behavior, but **League capture, input, latency, and the
workspace transition have not been validated in a live match**. True
exclusive-fullscreen games may not be capturable by Windows Graphics Capture;
borderless fullscreen is the first mode to test. No anti-cheat bypass is part
of this project. League continues to run natively on Windows.

## Host-only fallback

**Open on Windows host (Preview)** opens a Start Menu shortcut on the Windows
desktop without creating an Omarchy window. File Explorer and League host
shortcuts are also available. This uses a separate local-only bridge on port
4456 that accepts fixed names or opaque catalog IDs, never arbitrary guest
paths or command lines. Use the Windows task switcher to return to Omarchy.

## Getting the guest menu

New guest images built with [guest patch 0083](../guest-build/0083-Integrate-Windows-Apps-in-the-guest-menu.patch)
include the per-window preview, host-only fallback, and optional desktop
stream in the Omarchy menu. The Windows launcher starts the host bridges when
the VM starts. The published v0.0.20 preview guest image predates this patch.
For an existing guest, copy [`scripts/windows-apps`](../scripts/windows-apps)
into the shared Windows folder as `windows-apps`, then run this once from an
Omarchy terminal:

```sh
bash "$HOME/Omarchy Shared/windows-apps/install-in-guest.sh"
```

The installer adds commands and menu entries to the current guest user's home.
If the shared folder has a different name, use its actual path. Restart the app
menu if the entries do not appear immediately.

## Optional whole-desktop preview

**Windows Desktop (Preview)** uses [Sunshine](https://docs.lizardbyte.dev/projects/sunshine/latest/md_docs_2getting__started.html)
on Windows and [Moonlight](https://github.com/moonlight-stream/moonlight-qt)
inside Omarchy to show an entire Windows display. It does not turn each Windows
app into an Omarchy window. In Omarchy's Windows Settings or tray, choose
**Windows desktop in Omarchy** to install or configure a local-only Sunshine
service. Sunshine's setup page handles credentials and pairing; keep the
password private. Moonlight's first launch downloads and verifies a pinned
AppImage, then extracts it because this guest lacks FUSE 2.

On the tested RTX 5080 host, Moonlight's GUI ran in CPU-rendering guest mode,
but the GPU-rendering guest repeatedly became unresponsive. Pairing, a full
stream, and game input remain unverified. Keep the Omarchy window on a
different display from the captured host display to avoid recursive capture.

## Hardware limits

The Linux guest's VirGL path translates OpenGL work to the host GPU, but it
does not expose the RTX 5080 as a native Linux PCI GPU. CUDA, OptiX, and
native GPU passthrough are unavailable through this VM configuration. Windows
and Omarchy share CPU scheduling while running; guest vCPU count and RAM size
are chosen at boot. The current Windows QEMU runtime does not physically
return ballooned guest RAM to Windows, so live guest RAM resizing is disabled.
An [isolated experimental runtime](evidence/BALLOON-EXPERIMENT-2026-09-26.md)
returned some resident RAM in a disposable guest test. It is not installed or
enabled in the normal app.
