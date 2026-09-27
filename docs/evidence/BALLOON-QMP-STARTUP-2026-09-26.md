# Disposable QMP startup timing diagnostic — 2026-09-26

The [opt-in diagnostic script](../../scripts/vmtest/balloon-qmp-startup.py) booted the inactive acceptance guest disk four times with QEMU `-snapshot`, the exact experimental executable SHA-256 `43160f86cbf28a67df6f529b104dd03f63a37ca5d3d2a9148f11358fbc559b0a`, WHPX, 4 GiB RAM, four vCPUs, `-nodefaults`, `-display none`, and one random loopback TCP QMP socket. SSH used a separate random loopback port. No normal Omarchy process, installed runtime, or installed disk was used. Each disposable process exited cleanly after an ACPI request; no QEMU process remained after the matrix.

| Boot | CPU | First QMP contact | SSH ready | QMP greeting and capabilities | End |
| --- | --- | ---: | ---: | --- | --- |
| [1](balloon-qmp-startup-20260926-1.json) | `host` | 6.25 s, immediately after SSH | 6.25 s | Immediate | ACPI exit at 7.74 s |
| [2](balloon-qmp-startup-20260926-2.json) | `host` | 30.17 s, after SSH stayed responsive | 6.24 s | Immediate | ACPI exit at 31.69 s |
| [3](balloon-qmp-startup-20260926-3.json) | production CPU fallback flags, `qemu64,+ssse3,+sse4.1,+sse4.2,+popcnt,+aes` | 30.23 s, after SSH stayed responsive | 6.25 s | Immediate | ACPI exit at 31.73 s |
| [4](balloon-qmp-startup-20260926-4.json) | `host` | 30.16 s, after SSH stayed responsive | 6.23 s | Immediate on first client | ACPI exit at 33.72 s |

Boot 4 also held its first handshaken QMP connection open while opening a **second TCP connection to the same QMP listener**. The second TCP connection completed, but its greeting read timed out after two seconds. Closing the first QMP connection caused the second to receive its greeting; its `qmp_capabilities` call then succeeded. This directly reproduces the *TCP connected, no QMP greeting* symptom when another client holds this one-client listener. It does **not** establish that another client caused the two earlier post-boot greeting timeouts: those attempts did not retain connection/accept traces, and their first-client ownership cannot be reconstructed.

In this small matrix, neither a first connection delayed to 30 seconds nor switching from `-cpu host` to the production CPU fallback flags reproduced the timeout. The verified bundle's console QEMU `-display help` lists `none`, `sdl`, `egl-headless`, `curses`, and `dbus`, but no VNC backend, so a loopback VNC display comparison could not be run with this bundle. The diagnostic also differs from the normal GPU launch: it omits the SDL display path, guest input/audio devices, `-no-reboot`, and two of the three private Unix QMP sockets. These four clean boots do not overturn the launcher's warning about early WHPX QMP probes or clear normal-launch readiness.

A subsequent [actual Windows adapter check](BALLOON-ADAPTER-2026-09-26.md) used a dedicated private endpoint alongside three independently connected QMP monitors. That headless test passed without RAM resizing. It does not validate the normal graphical launch or clear the remaining graphics/gaming stability checks. No normal launch integration was changed.
