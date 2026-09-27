# Experimental native-window foreground handoff

Status: **implemented behind an explicit developer flag; build and physical
validation pending**. Normal launches and the normal runtime pin are unchanged.
The isolated recipe is documented in
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
links raising a window to foreground permission. Delegating permission from
foreground QEMU to the exact native app is therefore a testable explanation;
it is not yet a demonstrated fix.

## Opt-in launcher and private transport

The launcher requires `-experimental-native-foreground`, native presentation
mode, and exactly one configured display. This adds a fourth local AF_UNIX QMP
endpoint, `native.sock`; ordinary launches retain their three control endpoints.
Before QEMU starts, the experiment verifies the expected app-owned control
directory and installs a protected, inheritable DACL granting the current user
full control. Before each handoff connection it verifies the socket's owner,
DACL, private parent and AF_UNIX reparse tag. It does not alter unrelated paths.

The foreground command is discovered by exact name through `query-commands`.
Older runtimes take the ordinary placement failure path. Requests begin only
after the existing supervisor has connected QMP and observed guest readiness;
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
cannot use the handoff; a successfully acknowledged hidden layout can establish
one. Guest bootstrap handling must preserve that rule.

## Runtime operation

The separately pinned QEMU patch adds the downstream Windows-only command
`__omarchy_native-foreground-handoff`. SDL registers its exact single display
HWND and clears it before destruction and cleanup. QEMU checks:

1. Its registered display is the exact foreground, visible, non-iconic,
   non-topmost window on the active interactive desktop.
2. The target is a visible, non-iconic, non-topmost, responsive top-level native
   HWND with the requested PID and nonzero incarnation marker.
3. A held process handle identifies a live process with the expected creation
   `FILETIME`, in the same session and interactive desktop.
4. The expiry, display and native identity remain valid immediately before
   `AllowSetForegroundWindow` grants permission to that exact PID.

The operation uses no `ASFW_ANY`, input injection, topmost promotion, global
QEMU demotion, or `AttachThreadInput`. It does not move or focus a window.
Synchronous cross-process placement inside QMP could hang the VM; asynchronous
placement could outlive a lease, so that variant was not implemented.

After the reply, the host rechecks its live context, lease and identity, retries
ordinary placement once, and verifies z order. Windows can expire delegated
permission on subsequent input; see
[`AllowSetForegroundWindow`](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-allowsetforegroundwindow).
A QMP success is therefore not proof that the app was presented successfully.

## Remaining acceptance checks

- Compile the Windows runtime and launcher, and run the QAPI and ACL tests.
  The headless QMP smoke checks exact identity/display rejection errors but
  cannot exercise a foreground SDL window.
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
