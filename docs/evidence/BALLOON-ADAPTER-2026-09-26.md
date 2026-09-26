# Actual Windows balloon adapter, private QMP topology — 2026-09-26

The opt-in test in `app/experimental_balloon_adapter_windows_physical_test.go` ran **one** disposable 4 GiB Omarchy boot on the inactive acceptance disk with QEMU `-snapshot`, WHPX, `-no-reboot`, and no visible display. The executable SHA-256 was the pinned, physically verified `43160f86cbf28a67df6f529b104dd03f63a37ca5d3d2a9148f11358fbc559b0a`. The installed Omarchy runtime, disk, and launcher were not used or changed.

The test created a private experimental AF_UNIX endpoint **before** starting QEMU and supplied it in QEMU's startup arguments. Three other private AF_UNIX QMP endpoints represented supervisor, tools, and forward roles. Their clients each completed a QMP handshake and `query-status`, then stayed connected while the real `runExperimentalBalloonOnWindows` adapter connected to its separate endpoint. The adapter verified the running executable path, PID, exact hash, and active reclaim QOM property. It ran for 13.000 seconds, spanning two five-second controller sampling intervals, and returned the expected `context canceled` when the test stopped it. All three other QMP clients still answered `query-status` afterward.

| Observed check | Result |
| --- | ---: |
| Guest SSH ready | 6.31 s after process start |
| Reclaim QOM property | `true` |
| QMP guest-stat `last-update` before / after adapter | 1,790,444,203 / 1,790,444,222 Unix seconds |
| Guest available-memory statistic before / after | 3,496,857,600 / 3,721,101,312 bytes |
| QMP actual RAM before / after | 4,294,967,296 / 4,294,967,296 bytes |
| Windows available RAM before / after | 41,813 / 41,621 MiB |
| Guest SSH after adapter | Responsive |
| Other QMP clients after adapter | 3 of 3 responsive |
| Exit and cleanup | ACPI shutdown successful; test QEMU process exited; private endpoint directory removed |

The retained [raw result](balloon-adapter-20260926-result.json) has SHA-256 `b5907517e0d821dc3effa106fe708f599ba3038c60ddfec16204e1aaa0999fdc`. QEMU stderr contained one `Ignoring request for interrupt vector 0` warning and no balloon-specific errors.

This check validates the **actual adapter's** private endpoint lifecycle, QMP attestation, sampling path, and coexistence with three separate monitor clients. With about 41 GiB available on Windows, the production headroom policy correctly made no balloon request; automatic shrink/growth was physically tested separately with test-only thresholds. This headless snapshot boot does not exercise the normal SDL display or normal launcher startup, so it does not authorize normal-launch RAM integration.
