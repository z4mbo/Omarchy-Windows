# Direct Windows presentation in Omarchy

Decision: September 26, 2026, after the user explicitly accepted native Windows
windows drawn in Omarchy-assigned tiles. Rebuilding the VM or choosing different
technologies is allowed. The complete product is still unfinished.

## Rendering and layout

The working Hyprland desktop remains in QEMU. A small guest proxy window reserves
each application's place in the Hyprland layout. The guest reports the proxy's
rectangle and workspace visibility. A host controller moves the corresponding,
already granted Windows window over that tile. The Windows window remains a
top-level native window: no cross-process reparenting, frame polling, encoding,
decoding, application hooks, or synthesized application input is needed for this
presentation path.

Windows owns the application's pixels and normal device input. Hyprland owns the
layout proxy. This distinction matters: a screenshot taken only inside the VM
will see the proxy, not the native Windows pixels. Popups, focus, workspace
switching, and exclusive fullscreen need explicit integration and physical
tests. This is not evidence of universal compatibility or zero overhead.

## Alternatives considered

| Architecture | Useful capability | Reason for current decision |
| --- | --- | --- |
| QEMU Hyprland + direct Windows presentation | Actual Omarchy desktop plus native Windows rendering/input | First prototype; preserve the working desktop while testing the new integration |
| Windows tiling shell + WSLg | Native Windows and Linux app windows on one host desktop | Could recreate Omarchy's experience, but would replace actual Hyprland; not silently substituted |
| QEMU + WSL GPU companion | Working Hyprland and measured RTX CUDA in separate Linux processes | Promising Linux GPU path; interactive viewport and resource orchestration remain unverified |
| Full desktop under WSLg | Shared GPU and elastic WSL memory | Current Hyprland/Aquamarine dependency on DRM/GBM and DMA buffers failed on this machine |
| Ordinary Hyper-V VM with Linux GPU partitioning | Potential guest GPU access | Unsupported client-host configuration and unverified presentation; no advantage established here |
| Per-window capture or full-desktop streaming | Remote pixels inside Linux | Existing preview has input/performance failures; direct presentation avoids that frame transport |

Primary references: [Windows window positioning](https://learn.microsoft.com/en-us/windows/win32/api/winuser/nf-winuser-setwindowpos),
[WSLg architecture](https://github.com/microsoft/wslg),
[WSL GUI support](https://learn.microsoft.com/en-us/windows/wsl/tutorials/gui-apps),
[Hyper-V GPU support boundaries](https://learn.microsoft.com/en-us/troubleshoot/windows-server/virtualization/troubleshoot-hyper-v-gpu-assignment-partitioning-passthrough-issues).

## Prototype contract

- Opt-in host environment: `OMARCHY_WINDOWS_PRESENTATION=native`.
- The existing per-boot bearer token and host-selected window grants remain the
  authorization boundary. The guest cannot provide a raw host window handle.
- `GET /v1/presentation` advertises `mode` and protocol version `1`.
- `POST /v1/layout` supplies a strictly increasing sequence, logical output
  dimensions, and at most eight granted window IDs with rectangles and visibility.
- A single guest controller submits the complete desired layout periodically,
  including off-workspace entries with `visible: false`. Omitted entries are
  released. A short lease restores native windows when the guest stops responding.
- The host maps logical coordinates into its verified QEMU client area, validates
  every window identity and rectangle, and saves original placement before acting.
- The first implementation supports one guest output. Multiple outputs, uncertain
  scale/transform, or stale layout must fail visibly rather than position windows
  on an unrelated screen.
- Closing/revoking the bridge restores owned placements. Ordinary host windows
  must remain usable after a stopped guest, failed request, or launcher shutdown.

## Implementation checkpoint

The opt-in host controller, guest GTK layout proxies, mode negotiation, and
Super-key routing are implemented. Guest compatibility revision 34 packages the
base helpers and revision 35 adds optional floating-window occlusion support.
Native mode rejects the old frame and application-input endpoints.
Normal typing and clicks go to the actual Windows window. Omarchy Super-key
chords use a separate ordered QMP transport that releases possibly held keys
after a connection failure or queue overflow.

Twenty-four guest helper tests pass, including the copies embedded in the
revision-35 patched source. Four image-smoke helper tests and three offline
performance-analyzer tests pass locally.
The full native Windows Go suite passes, the Windows launcher builds, and its
static analysis passes. Automated checks
cover bounds, host grants, hidden-window retention, layout acknowledgements,
key routing, transport recovery, and bounded restoration bookkeeping. These
checks do not prove actual window placement, text entry, gameplay, or latency.
Desktop validation remains pending in an isolated portable copy.

The September 26 second candidate executable has SHA256
`3720313ff51220f1470375712e080b63b36da3219766bb23d49fc07811acb939`.
It was built locally after the host implementation at `d79c914`; it has not
replaced the normal installation. The isolated writable overlay and separate
runtime are prepared. The user has since explicitly resumed Computer Use.
Automatic approval review still rejected the isolated VM launch with only
`blocked by policy`; the VM has not been started. This is a tool execution
block, not a withdrawn user authorization.

Current scope is one unrotated guest output at 100% guest scale and at most eight
host-granted windows. Host coordinates use per-monitor DPI awareness. Topmost
host windows are rejected by this prototype. Protocol 1 now optionally advertises
tile-relative occlusion rectangles. A newer guest clips only when the host
advertises that capability; older clients and hosts retain their existing format.
The candidate clips overlaps from floating Linux clients above tiled native
proxies. Uncertain floating stacking hides native windows with an explanation.
Custom host window regions and right-to-left region coordinates are rejected
before clipping. This has pure geometry and recovery tests, not desktop proof.
Native windows can still cover launcher layers and unreported GTK/XDG popups;
popup ownership and exclusive fullscreen need further work.

Computer Use opened and inspected standalone Windows Character Map after the
user resumed testing. Its pointer test did not complete: the helper first
reported intervening user input, then `foreground window did not report a
process id`; recovery no longer found that window. No successful native input
or integrated desktop result is inferred from that attempt. See the
[benchmark procedure](NATIVE-PERFORMANCE-BENCHMARK.md) for measurements still due.

CI now builds and boots a fresh image when a pull request or push changes guest
patches or image test/build inputs. The existing manual image-build input is
retained. This adds an automatic future-update check; old-disk upgrade, rollback,
and physical Windows acceptance remain separate gates.

## Acceptance before replacing the installed app

1. A real Windows app and a Linux app tile side by side on the Omarchy desktop.
2. Text, mouse, and raw-input behavior use the actual Windows window, with no
   frame/input endpoints called in native mode.
3. Workspace changes hide and restore the correct windows; guest menus and Linux
   windows can obtain focus without stale native windows covering them.
4. Resizing, monitor scale, native popups, fullscreen transitions, and return from
   fullscreen preserve position and workspace association.
5. Stop/revoke/crash/lease-expiry paths restore windows and leave unrelated
   Windows apps untouched.
6. Compare the same Windows workload standalone and integrated using frame time,
   frame rate, image quality, CPU load, and input latency. Direct rendering alone
   is not a performance benchmark.
7. Separately validate interactive Linux GPU development/rendering, dynamic RAM
   behavior, installation, recovery, and a signed one-app release.
8. Validate supported future Omarchy upgrades against the host/guest protocol,
   Hyprland state format, managed integration files, graphics runtime, and
   resource controls. Preserve user data and settings, stage changes before
   activation, and retain recovery for failed upgrades. Automatic rollout must
   follow a successful fresh-boot, upgrade, and rollback validation matrix.

The existing disk is retained while this prototype is tested. Replacement waits
for a demonstrably working candidate, not just successful unit tests.
