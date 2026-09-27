# Windows applications as Omarchy windows: architecture and limits

## Current direction: direct native presentation

The user has accepted Windows drawing each real application directly in an
Omarchy-assigned tile while Hyprland controls its placement and workspace.
The [native presentation prototype](NATIVE-WINDOW-PRESENTATION.md) therefore
exchanges layout metadata rather than capturing and relaying application pixels
and input. This decision changes the earlier requirement that every app surface
cross into Linux. The capture architecture below remains the installed preview
and explains the limitations that motivated the new direction.

## What the feature means

Windows 11 remains the host. A Windows application, including a game, executes
on that host with its Windows drivers. The target design exchanges pixels,
window metadata, audio, and input through a local bridge. A Linux presenter owns one ordinary
Hyprland surface per granted Windows top-level window, so Hyprland can tile,
move, and fullscreen that surface. The process has **not** moved into Linux;
Windows still owns its window, input focus, GPU resources, and security policy.

The requested claim that *every* Windows application will behave exactly like a
native Omarchy application is **not technically supportable**. Windows lets an
application exclude its window from capture with
[`WDA_EXCLUDEFROMCAPTURE`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowdisplayaffinity),
and the desktop duplication API protects protected video content. Apps also use
different input paths, including raw input rather than ordinary posted window
messages. Broad compatibility with per-app results is a possible narrower scope,
but the user explicitly rejected exceptions on September 26, 2026. It is **not
the accepted completion criterion**. The universal requirement remains unmet;
see [the requested scope](FORK-GOAL-STATUS.md). [Microsoft's desktop duplication
documentation](https://learn.microsoft.com/en-us/windows-hardware/drivers/display/desktop-duplication-api)
explicitly describes protected content handling.

## Current implementation and evidence

The host launcher grants specific top-level HWNDs, passes a per-boot token to
the guest, and serves frames and bounded input on a local bridge. The guest's
GTK4 presenter creates separate non-floating Hyprland clients. The default
host capture calls `PrintWindow`, then sends PNG over HTTP at preview speed;
an opt-in Windows Graphics Capture (WGC) prototype also exists. Microsoft's
[`PrintWindow` documentation](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-printwindow)
says the target application renders the supplied device context; it is not a
general game-frame API. WGC can create an item for one HWND through
[`CreateForWindow`](https://learn.microsoft.com/en-us/windows/win32/api/windows.graphics.capture.interop/nf-windows-graphics-capture-interop-igraphicscaptureiteminterop-createforwindow).

On one Windows 11 / RTX 5080 host, Notepad, File Explorer, Character Map, and an
idle League client appeared as separate guest windows. A synthetic borderless
fullscreen window filled a guest workspace. The visible Character Map grid
click selected the wrong character; modern Notepad ignored posted text. No
League match frame, player input, fullscreen transition, or latency has passed
a live test. These are [recorded observations](evidence/WINDOWS-HOST-INTEGRATION-2026-09-26.md),
not evidence of general compatibility.

## Why input is harder than capture

The existing bridge posts `WM_MOUSE*`, `WM_KEY*`, and `WM_CHAR` to the chosen
HWND or child HWND. This can work for classic controls but is not physical
device input. Microsoft describes [`WM_INPUT` and raw input](https://learn.microsoft.com/en-us/windows/win32/inputdev/about-raw-input)
as a separate path used by applications that register devices. Microsoft's
[Windows UI automation guidance](https://learn.microsoft.com/en-us/windows/apps/dev-tools/winapp-cli/ui-automation)
also notes that WinUI/UWP windowless XAML controls can ignore posted character
and key messages even if `PostMessage` succeeds.

`SendInput` is closer to physical input, but it inserts events into the
**system-wide** input stream and is restricted by process integrity levels.
Windows directs keyboard input to the foreground window, and
[`SetForegroundWindow`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setforegroundwindow)
is restricted. With QEMU/Omarchy and a host game sharing one interactive
Windows desktop, letting the game own foreground focus conflicts with using
QEMU as the ordinary keyboard target. A host input broker and explicit focus
handoff might solve particular cases, but it has not been proved for games or
simultaneous Windows and Omarchy use. The bridge must never claim success just
because it queued an input message. See Microsoft's
[`SendInput` documentation](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-sendinput).

The Windows secure desktop is another boundary: UAC prompts and sign-in
appear on the Winlogon desktop, which this ordinary application cannot access.
Some privileged remote-desktop implementations can handle secure-desktop
transitions. That does not turn them into independent windows on the ordinary
desktop or prove that every protected surface can be presented in Hyprland.
[Microsoft's desktop model](https://learn.microsoft.com/en-us/windows/win32/winstation/desktops)
describes that separation.

## Architecture to build and validate

1. **Window lifecycle.** Keep the per-window grant and opaque ID model. Track
   process trees and owned top-level windows so a launcher, dialog, or new game
   HWND can inherit the intended Hyprland workspace. Revoke access on close,
   process exit, or user request. Never automatically grant unrelated reused
   single-instance windows.
2. **Geometry and visual correctness.** Fix coordinate mapping with real HWND
   client bounds, capture crop, DPI, and presenter size. The Character Map
   mismatch is a release blocker for the current preview. Track window resize,
   minimize, monitor movement, cursor shape, and popup/child-window ownership.
3. **Capture.** Move ordinary GPU-rendered windows to a persistent WGC frame
   session per granted HWND. Keep `PrintWindow` only as an ordinary-app
   fallback. For game mode, prototype a low-latency GPU frame path with hardware
   encoding and guest decoding; PNG polling at ~6.7 frames/s cannot be called
   gaming. Handle capture refusal, blank frames, device loss, and resize as
   explicit states. Desktop duplication may help with fullscreen *display*
   capture, but it captures a monitor, not an isolated per-app window, and can
   be invalidated by display-mode or desktop changes. See the
   [DXGI duplication contract](https://learn.microsoft.com/en-us/windows/win32/api/dxgi1_2/nn-dxgi1_2-idxgioutputduplication).
4. **Input.** Keep posted-message input for tested classic Win32 controls.
   Prototype a separately gated host input broker for WinUI and games, with
   keyboard scan codes, mouse deltas/buttons, gamepads, IME, cursor capture,
   foreground handoff, and an immediate escape back to Omarchy. Prove the
   target received input, including after switching Hyprland workspaces.
   Do not inject into elevated or secure desktop targets or bypass app policy.
5. **Audio and fullscreen.** Route per-app audio only after the visual/input
   path works. Detect a game's new borderless/exclusive window; if capturable,
   create a separate guest surface in its launcher's workspace and request
   Hyprland fullscreen. Test focus, alt-tab, resize, cursor confinement, and
   returning to the client. If capture or input fails, show a specific error and
   offer a whole-desktop view or direct Windows view rather than a blank tile.
6. **Compatibility gate.** Test native Win32, WinUI/UWP, Electron, browsers,
   protected media, elevated apps, multi-window tools, borderless games, and
   exclusive-fullscreen games on several GPU/driver combinations. Record frame
   rate, encode/decode latency, click accuracy, text/IME, gamepad, audio, focus
   transitions, and a sustained session. Publish a per-app matrix and claim
   support only for configurations that pass it.

The separate Windows session / RemoteApp route is not a drop-in solution for
the user's Windows 11 host. Microsoft's
[supported RemoteApp hosts](https://learn.microsoft.com/en-us/windows-server/remote/remote-desktop-services/remotepc/remote-desktop-supported-config)
are Windows Server editions. A separate session would also need game GPU,
capture, audio, and anti-cheat testing before it could replace the local
bridge.

## League of Legends specifically

Keep League and Vanguard on the physical Windows host. Riot says Vanguard is
required while the League client is open and during gameplay, and Riot
[does not support Vanguard inside virtual machines](https://support.riotgames.com/en-us/riot/client/error-van-9100).
See the [League Vanguard FAQ](https://support.riotgames.com/en-us/league-of-legends/performance/riot-vanguard-faq-league-of-legends).
Host execution avoids the known *in-VM execution* prohibition, but it does
**not** establish that Riot permits or that this bridge can control a streamed
match. A live, policy-compliant match test is required before advertising
League gameplay. No anti-cheat bypass belongs in this project.
