# Fresh guest candidate on Windows — 2026-09-26

This is an unsigned engineering candidate from [CI run 36253111377](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36253111377), source commit `a9ef634f9d14092dbb60c21a3e1c0d8f7966b137`. The CI guest contract, launcher tests, Windows launcher tests, full image build, and Linux KVM desktop boot completed successfully. The physical Windows check below used a direct, headless QEMU launch; it was not a one-click launcher installation.

## Artifact and runtime verification

| Item | Verified SHA-256 |
| --- | --- |
| GitHub artifact ZIP, 2,175,069,330 bytes | `921a0f17e94f6ad07645d8219ac7fce49ef65dae9fe6125bd3fde23460c7e606` |
| `rootfs.ext4.zst` | `1917675f34b81c8f924293662eafb87d29f2ddb9a08e3b749144b0291f180e89` |
| Decompressed `rootfs.ext4`, 7,516,192,768 bytes | `3ac5a678c65359879df5af91c8a8eb5b22710e903a859380435b45cfb37f7dc4` |
| `vmlinuz-linux` | `8ebc2c71271e000540f0a7545afd8e73504fcb724671f3d38c720b241842b901` |
| `initramfs-linux.img` | `6ebf43bcaa2bdbbfba09c35dea239ef217386b57441ecf57abc1f25d86d9cf81` |
| Pinned runtime portable ZIP | `8b0e198356dd4362478f91f6cebf0e71e7b59558829233f6ebd9b35e9b4debdc` |
| Installed runtime QEMU executable used for this test | `7b3aad5f42c1e7287ffb80dcbe73882deb8fe797cc55feff0ab8f5e1f53c0643` |

The downloaded ZIP matched GitHub's artifact digest. Every extracted guest file matched the artifact's `SHA256SUMS`; the runtime portable and source ZIPs matched `guest-build/runtime.lock.json`. The local combined `SHA256SUMS`, with both runtime archives added, has SHA-256 `853307be30cd64ca062105e162c41400b4204de5f6fd0babeffd79b7b44d6707`. This combined manifest is a local test input, not a published signed release manifest.

## Physical boot result

The candidate rootfs was the read-only backing file for a **disposable QCOW2 overlay** under `%LOCALAPPDATA%\Temp\omarchy-fresh-candidate-36253111377`. The bundled WINQ-EMU runtime booted it with WHPX, 4 GiB RAM, 4 vCPUs, `-display none`, CPU guest rendering, a separate QMP socket, and SSH forwarding on port 2245. The normal installation's disk, launcher, runtime, settings, registration, and QMP socket were not replaced. No user password was used.

The first boot reached the `omarchy` instant-trial account over the test SSH key. Checks inside that guest reported:

| Check | Result |
| --- | --- |
| Kernel and guest compatibility | `7.2.7-arch1-1`; `32:7.2.7-arch1-1` |
| Omarchy version | `4.0.3` |
| Hyprland | Running, one `Virtual-1` output at 1280×720 in this headless test |
| Windows-app menu | Five `.desktop` entries; launch helper executable |
| Per-boot seamless token | Service enabled; expected disposable test token present, readable by the guest user, `root:video:640` |
| Disposable guest shutdown | Clean `reboot: Power down`; QEMU process exited |

The five guest entries were the Windows Apps menu, Windows desktop, File Explorer, League, and open-app shortcuts. `desktop-file-validate` accepted them; File Explorer emitted a nonfatal category hint. The copied files and probes remain in the disposable test directory for review.

## Isolated first-install setup acceptance

A separate Windows test ran the launcher's **actual portable first-install setup functions** against the same verified candidate payload. The candidate-only harness is `app/candidate_acceptance_windows_test.go` (`windows && candidate_acceptance` build tags). It created a never-used `%LOCALAPPDATA%\Temp\omarchy-fresh-candidate-36253111377\setup-acceptance-data` directory and invoked `chooseProvisionMode` (instant), `ensureRuntime`, `ensureGuest`, and `prepareDisk`. It seeded an inert progress object to avoid opening the Win32 splash. An unreachable loopback release URL was used so an accidental online fallback would fail. No normal Omarchy installation path, registration, shortcut, desktop, or VM process was changed.

The command was `go test -tags candidate_acceptance -run '^TestCandidateFreshPortableSetup$' -count=1 -v .` from `app`, using Go 1.27.1 and the three `OMARCHY_CANDIDATE_*` environment variables for the verified payload directory, new data directory, and local combined manifest digest `853307be30cd64ca062105e162c41400b4204de5f6fd0babeffd79b7b44d6707`. The test passed in 18.6 seconds (17.67 seconds in the test body). It verified:

| Setup check | Result |
| --- | --- |
| Runtime unpack and receipt | Matched pinned portable archive digest `8b0e198356dd4362478f91f6cebf0e71e7b59558829233f6ebd9b35e9b4debdc`; extracted QEMU executable SHA-256 `7b3aad5f42c1e7287ffb80dcbe73882deb8fe797cc55feff0ab8f5e1f53c0643` |
| Guest copy, decompression, and receipt | Passed; installed 7,516,192,768-byte rootfs SHA-256 `3ac5a678c65359879df5af91c8a8eb5b22710e903a859380435b45cfb37f7dc4` |
| Writable disk | 24 GiB QCOW2 with `../guest/rootfs.ext4` backing; backing identity sidecar matched rootfs SHA-256 |
| Second setup pass | Runtime receipt, guest receipt, and disk modification times unchanged |
| Independent disk inspection | Bundled `qemu-img info --output=json` reported QCOW2 compat 1.1, `corrupt: false`, 25,769,803,776 virtual bytes, and 196,992 actual bytes |

The same candidate-only harness then tested the **ordinary nonportable setup path** in a second never-used `%LOCALAPPDATA%\Temp\omarchy-fresh-candidate-36253111377\setup-acceptance-standard` directory. A loopback HTTP server served the verified files so the real `ensureRuntime` and `ensureGuest` download and manifest paths ran without external network access. `prepareDisk` created the standard sparse `disk.raw`. The command `go test -tags candidate_acceptance -run '^TestCandidateFreshStandardSetup$' -count=1 -timeout 15m -v .` passed in 26.2 seconds (25.38 seconds in the test body). It verified the runtime and guest receipts, a SHA-256 match across the raw disk's entire 7,516,192,768-byte factory prefix, and an unchanged second setup pass. `fsutil sparse queryflag` reported that the 24 GiB disk is sparse. Bundled `qemu-img info --output=json` reported raw format, 25,769,803,776 virtual bytes, and 6,089,080,832 actual bytes.

Together, these tests validate portable and ordinary archive handling, manifest trust, decompression, installation receipts, disk preparation, and repeat setup on the physical Windows host. They do not validate the complete one-click installer path: launcher UI choices, Windows hypervisor enablement, shortcuts, bridge startup, signed publishing, and subsequent interactive VM use remain outside these isolated tests.

## Limits and isolation observation

This boot proves that the **new complete image starts on this Windows WHPX host** and contains the guest integration. Headless direct QEMU did not exercise the Omarchy one-click installer, signed update path, host Windows-app bridge, native app input/capture, GPU acceleration, games, dynamic RAM adjustment, or matching the real monitor resolution and refresh rate. The 1280×720 virtual output is a test display, not the user's 2560×1440 at 360 Hz monitor.

At 18:33:05, the separately installed Omarchy launcher's log recorded `close confirmed - graceful guest shutdown`, followed by a clean poweroff at 18:33:12. This preceded the disposable guest's SSH-triggered poweroff at about 18:33:39. The direct-QEMU command and scripts used only the candidate QMP socket and SSH port 2245; they did not target the normal QMP socket or SSH port 2244. The normal launcher's close guard logs that message only when its shutdown confirmation returns Yes. The source of that confirmation was not established. The normal VM was not restarted during this check.
