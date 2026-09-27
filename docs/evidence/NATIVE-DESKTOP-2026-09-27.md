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

A later mouse-only check opened **Apps** from the Omarchy logo, scrolled to
**Windows Apps in Omarchy**, and clicked it. The Windows picker opened as a
Linux tile beside the existing proxy. This confirms the desktop menu route
works with the development helpers; the first picker test had launched the
helper through compositor IPC.

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

Restarting the controller exposed a separate ordering defect: the real window
could stay behind QEMU while the bridge accepted a visible layout with an empty
suspension reason. Explicitly activating Character Map revealed the correctly
positioned native window. Returning to a workspace alone is not a substitute
for verifying controller restart recovery; this case needs a new host build
and a physical retest.

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
4. Selecting File Explorer in the picker created a new Windows File Explorer
   window, confirmed through Computer Use's returned window and accessibility
   state. The bridge still listed only Character Map, and Hyprland had no
   Explorer proxy. The test window was closed without opening or changing a
   file. This build does not automatically attach Explorer's reused-process
   window; fresh-window correlation needs a separate fix from Blender's direct
   child-process tracking.

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
- Physical Windows validation using the complete current factory image,
  followed by a complete installer test.

## Follow-up CI and idle preference

[CI run 25](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36318844583),
source commit `918bb56cb5fc987ed57d2d206d536b12bccfaeef`, passed the launcher,
Windows tests, guest contracts, and complete revision-38 image boot. Its
five-boot existing-disk sequence also passed: seed, upgrade, reboot, boot with
the older image, and return to the candidate. The logs confirm Zenity is
package-owned after the upgrade and retained across subsequent boots, while
the test documents, settings, and Neovim configuration remain intact. This is
Linux KVM image/upgrade evidence, separate from Windows desktop acceptance.

The downloaded `guest-upgrade-evidence` artifact, ID `10932241001`, matched
GitHub's archive SHA256
`bd3d9a66bf579e4bf56a277851da5a3fbbfd497ae8e1d341dbb85f01395f9cc1`.
The same run subsequently passed interrupted package-write recovery. Its
`package-write-recovery-evidence` artifact, ID `10931892664`, matched archive
SHA256 `383999df52eec11a21c4a9b67ff9400d9ec54c265b23e79b8c83a4de95d285a6`.
The receipt reports `passed: true` for seed, cut, torn, restore, and reboot,
with an unchanged source disk chain. This remains a Linux CI recovery test;
it does not establish automatic Windows recovery from every failed update.

The user subsequently requested disabling Omarchy's automatic locking. After
the user unlocked the test VM, the supported `omarchy-shell idle disable`
command enabled Stay Awake. Its status returned `enabled: false` and
`stayAwake: true`, with idle timers stopped. The preference file
`~/.local/state/omarchy/indicators/stay-awake` was present. This first-party
preference controls both the automatic screensaver and idle lock.

Guest patch 0093 seeds this preference only in the new-user skeleton and raises
the compatibility revision to 39. It does not rewrite existing users' choices.
The new factory-image default subsequently passed CI and the fresh physical
test described below.

## Candidate 5: complete factory image and Blender

[CI run 26](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36320246763),
source `70ab843774f2ed8614c284d241fe06ed4fb4cea8`, passed all applicable jobs:
Windows and Linux launcher checks, guest contracts, a complete revision-39
desktop boot, five-boot existing-disk preservation, and interrupted package-write
recovery. Those recovery tests run under Linux KVM.

A new isolated portable test directory was prepared on the physical Windows
host using this run's launcher and complete factory root filesystem. The
launcher SHA256 was
`f46675a78ade17a05a32f9e4ba175a8e2c85f6dbf3345d1d118f2b8b7e9b122b`.
The compressed root filesystem SHA256 was
`4a24d33249f88b7024df1be0adaf7edeb34aae1a05221b0f9a5ea823cf1e8248`.
Each bounded artifact ZIP was checked against its GitHub digest, then the
ordered parts against the parts index and the original guest manifest. The
combined local guest/runtime manifest SHA256 was
`ada2693e2211c9468ed1a1e2f10eac0e8cc08f30eab117c4c52e4e77942dc8e5`.
The previously verified runtime was reused; no development guest helper was
installed into this disk. This remains an unsigned test build.

The launcher created the fresh test disk, started the desktop, and signed in
automatically. The packaged Stay Awake default was present: idle disabled,
Stay Awake enabled, timers stopped, and lock state false. Hyprland reported
2560×1440 at 360.039 Hz, matching the host's 2560×1440 / 360 Hz mode to virtual
mode rounding. This verifies the current single-monitor factory default, not
live monitor changes or multi-output behavior.

The packaged `omarchy-windows-open` desktop command was started through
Hyprland IPC. Computer Use then selected **Blender 5.2** in the visible guest
picker and confirmed launch. The host granted the descendant `blender.exe`
window automatically, and it appeared inside the Omarchy tile with the guest
bar and borders visible. Native keypad input changed the viewport to Right
Orthographic. This fixes the earlier launcher-to-child grant failure for the
tested Blender shortcut.

Two integration failures remain in this candidate:

- Initial maximized Blender geometry was reported as fullscreen, while the
  Hyprland proxy remained windowed. Blender's own **Window → Toggle Window
  Fullscreen** changed the native window, but did not produce the required
  matching proxy state. The host classifier and guest mapping lifecycle need
  their subsequent source fixes and a new physical test.
- Moving the proxy to another workspace hid Blender. Clicking that workspace
  in Omarchy's bar failed to restore it above QEMU: the proxy and an HTTP 409
  message appeared instead, and the host logged `window remained behind QEMU
  after z order repair`. Graceful guest shutdown restored the native Windows
  window, which was then closed normally.

A separate restart of the same candidate with the temporary environment
setting `SDL_ALLOW_TOPMOST=0` reproduced the workspace-return failure even
without toggling Blender fullscreen. That setting was confined to the test
process and is not a product fix. SDL's topmost setting therefore does not
resolve this observed failure. Window-order and foreground diagnostics are
needed before claiming workspace restoration works broadly.

No gameplay, frame-time comparison, latency measurement, or native-performance
equivalence was established. The existing personalized installation was not
replaced.

## Candidate 6: fullscreen classification and foreground diagnostics

The launcher from [CI run 27](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36322084452),
source `528313df374dda943154c6998e3c6c5fa9eb0afe`, was tested with the existing
isolated revision-39 disk and runtime from Candidate 5. This is a host-only
comparison, not a physical test of the revision-41 image built by the same run.
Launcher artifact `10932553967` matched GitHub's ZIP digest
`483969388f9b2d3eed550a08a0be7959d1d9b28a560b19195fe9c664eb52556c`.
The extracted executable matched SHA256
`7c4d3f5e1f2fc121fa0883e39832acf8ab43cabd07398d2b1da16f9742da0d24`.

The packaged picker again launched and granted Blender's child window. The
initial maximized application correctly reported `fullscreen: false`; the
Hyprland proxy was also windowed, at 2536 by 1390. This narrowly verifies the
maximized-window classification fix. Computer Use opened Blender's Window menu,
but the subsequent observation found QEMU foreground with the native window
behind it and a layout HTTP 409. The intended fullscreen toggle was not
performed, so matching fullscreen behavior remains unverified.

The new bounded host diagnostic established that both QEMU and Blender were
valid, visible, non-iconic, non-topmost windows at failure. QEMU's exact display
HWND was foreground. Its extended style was `0x10`; Blender's was `0x100`.
The diagnostic repeated at 30-second intervals rather than each layout poll.
This rules out a topmost QEMU window in this reproduction. A foreground-process
handoff is the next experiment, not an established fix; see its
[design and validation requirements](../NATIVE-FOREGROUND-HANDOFF.md).

Graceful guest shutdown restored the native Blender window. Computer Use then
closed the untouched default scene normally and confirmed its window exited.
The existing personalized installation and its settings remain untouched.
