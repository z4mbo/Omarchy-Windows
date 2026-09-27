# Native Windows tiles: physical desktop test, September 27

This is a partial acceptance record for the opt-in direct Windows presentation
path. It does not establish gaming compatibility or native-equivalent speed.
The normal personalized installation has not been replaced.

## Candidate and setup

- Windows 11, i9-14900KF, RTX 5080, primary 2560×1440 monitor at 360 Hz.
- Isolated portable `OmarchyNativeCandidate4.exe`, SHA256
  `f8851a6500f65e5bb5d826ebf0e763648d3b0f521132176ed3fff1e443025ec0`.
- Authenticated candidate image from CI run `36253111377`, factory rootfs SHA256
  `3ac5a678c65359879df5af91c8a8eb5b22710e903a859380435b45cfb37f7dc4`.
  The factory image has compatibility revision 32. Current development helpers,
  including the Lua workspace adapter from `57154a7`, were installed in the
  disposable guest user's home. This is a mixed development setup, not an
  acceptance test of the complete revision-38 image.
- Native presentation enabled, 8 vCPUs, 8 GiB RAM, GPU rendering, fullscreen,
  instant trial user and a separate writable QCOW2 disk/shared folder.
- Hyprland 0.56.2 reported 2560×1440 at 360.039 Hz and scale 1. This verifies
  the advertised mode on this monitor, not delivered application frame rate.
- The normal launch succeeded. Earlier VM startup approval failures no longer
  prevented this test after the user resumed work.

## Observed results

Computer Use selected Character Map in the guest's Windows app picker and
clicked OK. The host granted the new `charmap.exe` window. Hyprland listed its
native proxy on workspace 2 at `[12, 38]`, size `[2536, 1390]`. The real Windows
window occupied the corresponding tile, inside the visible Omarchy borders and
workspace bar.

Clicking its actual editable field and typing `Omarchy native input test`
succeeded. The Windows accessibility value and visible text agreed. The host
bridge's frame endpoint returned HTTP 409 after native activation, confirming
this test did not use the old PNG capture/input path.

Clicking workspace 1 in Omarchy's bar hid Character Map. A fresh desktop
observation showed only the wallpaper, and Character Map was absent from the
visible Windows window list while its bridge grant remained. Clicking workspace
2 restored the same app and its text. Opening the picker again tiled the
Character Map window on the left and the Linux picker on the right.

A controlled crash test killed only the isolated guest native controller after
checking its process identity. After the host's three-second lease expired,
Character Map returned to its original small Windows window and retained the
exact typed text. Computer Use observed the restored window and accessibility
value. This verifies one controller-crash restoration case; host launcher/QEMU
crashes and broader app recovery remain separate checks.

Initial observations immediately after actions sometimes captured the previous
frame or the empty Linux proxy. Subsequent observations showed the settled
window content. No latency or frame-time measurement was made.

## Issues found and repaired in source

1. The revision-32 image lacked `zenity`; the picker exited before displaying.
   Installing the signed `zenity 4.2.2-1` package in the isolated guest made it
   work. Patch 0091 adds it to the complete image package request and reviewed
   transaction lock. The manual helper installer now checks this prerequisite
   before making changes. Patch 0092 makes Zenity a dependency of the updated
   runtime package, so an existing disk receives it during the normal Omarchy
   package update; copying the new image alone does not install packages.
2. This Hyprland version uses Lua dispatch. The old workspace command failed
   with its explicit Lua parser hint. Patch 0090 adds a bounded fallback and
   caches the accepted syntax. A live backend test invoked the installed helper
   to move the Character Map proxy from workspace 2 to 3 and back: both moves
   were confirmed through Hyprland's client state, and the helper selected Lua.
   This verifies compositor IPC, not a game client-to-match transition.
3. Blender 5.2 started from the guest picker, but its main window was not
   automatically granted. The shortcut targets `blender-launcher.exe`, which
   starts `blender.exe`. The candidate tracks only the initial PID. Blender
   therefore appeared as an ordinary host app and suspended the projected
   Character Map while foreground. Its untouched test session was closed.
   Descendant-process launch tracking requires a new host build and retest.

## Remaining checks

- Native Blender rendering and fullscreen, multiple native apps, File Explorer,
  games and launcher-to-game child windows.
- Popup/layer stacking, Super-key routing, close/revoke and host-crash restoration,
  monitor changes, mixed DPI and multiple outputs.
- Baseline comparisons for frame time, latency, image quality, CPU and GPU load.
- The Computer Use tool's injected keyboard events did not reach the guest
  Walker menu, although mouse clicks did. Native Windows text input passed.
  Earlier SDL tests recorded a similar automation-input limitation; physical
  keyboard behavior needs a separate check before drawing a product conclusion.
- Complete factory-image and existing-disk upgrade validation for the new
  dependency and workspace fixes, followed by a complete installer test.
