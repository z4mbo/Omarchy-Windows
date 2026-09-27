# Native multi-window focus design

Status: **proposal only; no focus-sync protocol or multi-window foreground
handoff is implemented or physically validated**. Candidate 11 must establish
that one granted window can return above QEMU before this design changes the
projector. The normal runtime and installer do not use this proposal.

## Current gap

The guest sends each proxy's geometry and visibility but no active proxy.
`scripts/windows-apps/omarchy_windows_native_layout.py` reads `hyprctl clients`
and monitors, while the host `app/native_projection_windows.go` iterates a Go
map of requested windows. That order is unstable. The experimental v2 handoff
requires exactly one visible native tile. Removing that check would choose an
arbitrary HWND, and it would not ensure the other visible HWNDs sit above QEMU.

Focus also flows in both directions. Clicking a real Windows HWND can make it
foreground without changing Hyprland's active proxy. Super shortcuts, close,
workspace actions and the next QEMU foreground return could then target a
different proxy. The verified Windows foreground HWND is authoritative state
for aligning the guest proxy; its cause is not required to do that.

## Optional protocol 1 extension

Advertise `capabilities.focusSync.version=1` in the authenticated native
`GET /v1/presentation` response. Only after that advertisement may the guest
add an optional top-level `focus` object to the full `POST /v1/layout`:

```json
{"focus":{"id":"<granted 32-hex ID>","epoch":"<opaque transition ID>","echoOf":null}}
```

The guest reads `hyprctl -j activewindow` alongside clients, monitors and lock
state. It matches the active window's address and controller PID to exactly one
proxy carrying that ID's marker. It emits a new epoch only when the verified
active proxy or workspace changes. Heartbeats repeat the epoch. Unknown,
unmapped, off-workspace, locked or fully hidden focus has no ID. A host-driven
focus reconciliation sets `echoOf` to the host observation epoch so it cannot
be reflected back as a new activation request. Use an opaque per-transition
value, not the layout sequence, because a controller restart can change its
local counter without a user focus transition.

The layout acknowledgement may add a separate observation:

```json
{"foreground":{"id":"<granted 32-hex ID>","epoch":"<host transition ID>","source":"observed|projector"}}
```

This is a report of a *known projected foreground HWND*, never a command to
raise it. The host returns an ID only for a current leased, visible, granted
HWND or its verified same-process owned popup, with live process creation,
thread, marker, rectangle and workspace visibility checks. An unrelated host
foreground window yields no ID. The host changes the observation epoch only
when the verified foreground state changes, not every layout poll. `source`
is diagnostic: `projector` marks this launcher's own v2 activation and
`observed` means an already-foreground app with no asserted cause. No new
low-level input hook or claim about whether a user clicked is needed.

For either source, the guest may reconcile Hyprland focus only after rechecking
the acknowledgement's layout sequence, grant, proxy address/PID, current
workspace, mapping, lock state and visibility. It dispatches focus to the exact
proxy address only within the already-active workspace, at most once for the
observed epoch and mismatch, then reads `hyprctl -j activewindow` to verify the
result. This changes guest focus without raising QEMU on Windows. It records
`echoOf` to suppress the round trip. It must never switch workspace or select
a hidden proxy merely because a stale host HWND is foreground. If the workspace
changes between validation and dispatch, the guest skips reconciliation.

## Host placement transaction

The host validates that `focus.id` names one granted, currently visible tile
whose exact HWND/process/marker remain live and whose region is not fully
occluded. It can request QMP permission only when QEMU itself is foreground,
the prior lease and shared short Apply deadline are live, and a new verified
guest focus epoch or QEMU foreground return edge calls for one attempt. A
heartbeat with the same focus and foreground state must not reactivate an app.
Revocation, an unrelated foreground window, lock, display suspension, stale
identity, or an unknown focused ID suppress the request.

First support pairwise nonoverlapping visible native tiles. Guest and host
both reject uncertain overlap; `hyprctl clients` does not establish a reliable
floating-window stack order. The host then needs a deterministic two-phase
`Apply`: validate and place every tile and occlusion region, make at most one
v2 permission request for the selected focus target, place other validated
visible HWNDs nonactivating above QEMU in stable order, activate the focused
HWND last, and verify every visible native HWND's geometry and z order.
Failure restores or hides the affected projection under the existing lease and
does not surface a revoked or off-workspace HWND. Fullscreen is one visible
tile; other native proxies must be hidden by verified guest state. Owned
popups retain the existing bounded owner-chain checks.

Older hosts reject unknown layout fields, so new guests must wait for the
capability before sending `focus`. Older guests omit it and retain the current
single-visible-tile experimental path. The extension does not change the
capture protocol or grant authority. Separate guest-image and compatibility
patches are required after development-script tests.

## First acceptance tests

Test two tiled native apps, each proxy selected in Omarchy, with QEMU and a
native app alternately foreground. Verify that each Windows app remains in
its own tile, Super/close/workspace actions use the matching guest proxy, and
repeated heartbeats do not steal focus. Click each native HWND, then use
Alt+Tab, workspace switch, lock, fullscreen, a guest focus change, and QEMU
return. Repeat with app-driven foreground changes, revoked grants, an owned
popup, an unrelated Windows app foreground, and overlapping floating proxies.
Record actual window order, focus source, lease generation and failure recovery
without titles or secrets. No performance or universal-app claim follows from these
tests alone.

Hyprland documents `hyprctl -j activewindow`, `activewindowv2`, and exact
window-address focus selectors in its
[hyprctl](https://wiki.hypr.land/Configuring/Advanced-and-Cool/Using-hyprctl/),
[IPC](https://wiki.hypr.land/0.41.0/IPC/) and
[dispatcher](https://wiki.hypr.land/0.54.0/Configuring/Dispatchers/) references.
