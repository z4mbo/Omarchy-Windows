# Experimental native-window foreground handoff

Status: **experimental; workspace return remains broken**. Candidates 9 and 10
failed physical foreground tests. A versioned launcher-parent handoff is now
committed in source, but its QEMU compilation and physical behavior are pending.
Normal launches and the normal runtime pin are unchanged. The isolated recipe is documented in
[`runtime-build/experimental/native-foreground`](../runtime-build/experimental/native-foreground/README.md).

## Observed failure

Candidates 5 and 6 hid Blender correctly when its Omarchy workspace became
inactive, but could not bring it back above the fullscreen QEMU display.
Candidate 6's bounded diagnostic established that both windows were valid,
visible, non-iconic and non-topmost, with QEMU's exact display HWND foreground.
The complete revision-41 factory image reproduced the failure. A temporary
`SDL_ALLOW_TOPMOST=0` experiment did not fix it. See the
[physical test evidence](evidence/NATIVE-DESKTOP-2026-09-27.md).

The projector uses `SetWindowPos` and verifies the native window's z order.
Microsoft's [`SetWindowPos` documentation](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowpos)
links raising a window to foreground permission. Candidate 9 showed that QEMU
accepted permission delegation to Blender, yet Blender stayed behind QEMU.
Candidate 10 then tried one activating `SetWindowPos` after the grant. Blender
still failed on workspace return, and Character Map failed on initial
projection. Both reported `permission_accepted_still_behind`.

## Opt-in launcher and private transport

The launcher requires `-experimental-native-foreground`, native presentation
mode, and exactly one configured display. This adds a fourth local AF_UNIX QMP
endpoint, `native.sock`; ordinary launches retain their three control endpoints.
Before QEMU starts, the experiment verifies the expected app-owned control
directory and installs a protected, inheritable DACL granting the current user
full control. Before each handoff connection it verifies the socket's owner,
DACL, private parent and AF_UNIX reparse tag. It does not alter unrelated paths.

The v2 foreground command is discovered by exact name through
`query-commands`. Older v1 runtimes take the ordinary placement failure path;
they cannot receive a v2 request. Requests begin only after the existing
supervisor has connected QMP and observed guest readiness;
the experiment does not probe QMP during early WHPX boot.

QMP is privileged VM control. These checks protect the transport and limit
what this launcher requests. They do not authenticate a QMP peer inside QAPI,
or establish a new boundary against a process already permitted to control it.

## Request lifetime and identity

The host bridge selects grants. Guest layout JSON cannot select arbitrary host
HWNDs or PIDs. Before sending, the projector rechecks its bridge grant, exact
HWND/PID/thread/process creation time, per-window incarnation marker, requested
rectangle, foreground QEMU identity, and live committed layout lease.

All attempts in one layout share a 250 ms budget, starting before layout
validation. The QMP deadline is the earlier of that budget and the prior
committed lease. After feature discovery, the host sends an absolute Windows
uptime expiry capped at 200 ms. QEMU rejects an expired deadline or one more
than 250 ms in the future, including a repeat check immediately before the
permission operation. This prevents a delayed queued request from delegating
permission after its request lifetime.

A visible tile state gets one attempt. A changed rectangle or hidden-to-visible
transition can retry after a five-second cooldown. Error recovery can itself
produce that hidden state, so this is a bounded retry, not a promise of one
attempt per deliberate user action. Revocation and identity changes remove
the attempt record. An initial visible layout has no committed lease and
cannot use the handoff; the guest controller first sends an acknowledged
hidden layout to establish one before presenting tiles.

## Runtime operation

The separately pinned v2 QEMU patch adds the downstream Windows-only command
`__omarchy_native-foreground-handoff-v2` and removes the v1 command. SDL
registers its exact single display HWND and clears it before destruction and
cleanup. At registration, QEMU identifies its actual launcher parent once,
holds a handle to that process, and records its PID and creation `FILETIME`.
QEMU checks:

1. Its registered display is the exact foreground, visible, non-iconic,
   non-topmost window on the active interactive desktop.
2. The target is a visible, non-iconic, non-topmost, responsive top-level native
   HWND with the requested PID and nonzero incarnation marker.
3. A held target-process handle identifies a live process with the expected
   creation `FILETIME`, in the same session and interactive desktop.
4. The request's launcher PID and creation time match the bound, still-live
   parent. Its exact tray HWND belongs to that parent, has the expected class,
   and is on the input desktop in the same session.
5. The expiry, display, target, and launcher identity remain valid immediately
   before `AllowSetForegroundWindow` grants permission to the launcher PID.

The QMP operation uses no `ASFW_ANY`, input injection, topmost promotion,
global QEMU demotion, or `AttachThreadInput`. It does not move or focus a window.
Synchronous cross-process placement inside QMP could hang the VM; asynchronous
placement could outlive a lease, so that variant was not implemented.

After a v2 reply, the launcher rechecks the live request deadline, lease,
grant, native HWND and QEMU foreground state. With exactly one visible native
tile, it calls `SetForegroundWindow` once on that exact granted HWND. A bounded
`WM_NULL` `SendMessageTimeout` gives cross-queue activation time to complete
before the launcher verifies foreground ownership and z order; Microsoft
describes this [asynchronous foreground transition](https://devblogs.microsoft.com/oldnewthing/20161118-00/?p=94745).
The launcher does not retry activation on each layout heartbeat. Multiple visible tiles fail this
experimental gate; explicit guest focus intent is not yet part of the protocol.
Windows can expire delegated permission on subsequent input; see
[`AllowSetForegroundWindow`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-allowsetforegroundwindow).
A QMP success is therefore not proof that the app was presented successfully.
The v2 runtime has not yet compiled in CI or undergone a physical desktop test.

## Remaining acceptance checks

- Compile the isolated v2 QEMU patch in CI and run its exact-command headless
  QMP smoke. Local patch application, QAPI generation, recipe hash checks and
  focused Go tests passed. The headless smoke cannot exercise a foreground SDL
  window or prove the Windows handoff succeeds.
- Verify the real single-output launch registers its expected SDL HWND, then
  compare workspace return with the flag disabled and enabled on a disposable
  disk. Confirm actual native input and fullscreen synchronization.
- Exercise revoked grants, app exit/reincarnation, expired leases, QMP delays,
  guest lock, QEMU restart, and a different foreground Windows app. Failure
  must leave the recoverable layout error and no stale window surfacing.
- Check latency under load and ordinary window ordering. The 250 ms shared
  budget may reject a slow attempt; increasing it needs evidence against the
  guest request timeout and lease lifetime.

The experiment is not a supported installer feature and establishes no game
performance or universal app compatibility claim.
