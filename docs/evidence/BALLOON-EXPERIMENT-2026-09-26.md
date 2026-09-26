# Experimental Windows RAM balloon check — 2026-09-26

The separate [experimental runtime workflow](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36243917936) compiled and passed its lightweight QEMU smoke test. Its portable archive SHA-256 was `77d532c6ab5d4745d98aa7dce3cfe042afc3f7201b6d719f9be942360acc99e0`, matching the workflow artifact's `SHA256SUMS`.

On the Windows 11 Pro test host, I booted the *inactive acceptance guest disk* with QEMU `-snapshot`, 4 GiB RAM, 4 vCPUs, WHPX, a virtio balloon, no display, and `OMARCHY_QEMU_BALLOON_DECOMMIT=1`. The installed Omarchy VM and runtime were not changed. The disposable guest reached the serial login prompt.

| Check | Result |
| --- | --- |
| QMP initial balloon size | 4,294,967,296 bytes |
| Requested size after inflation | 2,147,483,648 bytes; QMP reported that exact size within about 6 seconds |
| QEMU working set before / after inflation | 3,325,341,696 / 2,537,480,192 bytes, a drop of about 751 MiB |
| QEMU private bytes before / after inflation | 4,444,364,800 / 4,443,807,744 bytes; effectively unchanged |
| Requested size after deflation | 4,294,967,296 bytes; QMP reached that size and the VM remained running |
| Guest console | Serial login prompt remained available after the cycle |

I then booted the same snapshot configuration with the installed **stock runtime** and no experimental environment setting. It also reached the serial login prompt and reported a 2 GiB balloon target. Its QEMU working set was 4,716,679,168 bytes before inflation and 4,716,507,136 bytes after reaching 2 GiB: effectively no reduction. This control supports the conclusion that the experimental runtime can release resident guest RAM on this host. The two runs had different baseline working sets, and other host processes were active, so the measured reduction is not a precise amount of RAM that Windows can reallocate.

This first check was **preliminary**, not release validation: it did not put the guest under a memory workload or inspect guest data after the balloon cycle. The follow-up below added a small in-guest integrity workload. The production runtime remains unchanged.

## In-guest workload follow-up

I restarted the experimental guest from the same inactive disk with `-snapshot`, loopback-only SSH, and the same 4 GiB QEMU RAM. A Python process in the guest held 384 MiB of written data and rotated SHA-256 checks across 32 MiB of it every two seconds. I requested two 4 GiB → 2 GiB → 4 GiB balloon cycles while this process ran. Both 2 GiB and 4 GiB targets were reached. The guest remained reachable over SSH, reported about 395 MiB available at the first 2 GiB target, and completed all 120 integrity rounds with zero mismatches after the second expansion. The QEMU working set fell from 3,470,036,992 to 2,555,441,152 bytes on the first shrink. I requested an ACPI shutdown and confirmed the disposable QEMU process exited. This adds evidence that the experimental runtime returns resident memory and preserves this small workload during two cycles. It does not establish safety under sustained memory pressure, graphics work, or a game; the installed runtime remains unchanged.

## Attestation build and deflation bottleneck

The [second isolated build](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36250647899) compiled the new QOM attestation property and passed CI's memory smoke test. The workflow artifact SHA-256 was `7fb786e6d631f1100b5ec5166e456f9beb79b8c951857e53685dc361ec1d81df`, and its portable ZIP matched the bundled `SHA256SUMS` entry `eb58561e7725a998ea28262c1ec255443431c4aef12101db3cb46b7ceffcdbc3`. The Windows executable SHA-256 was `c4bb9371284621478c4a4f165b199621a1c38ad02af3d14b0754ebe5f2542662`.

The first physical QMP query found that the property is on `/machine/peripheral/experimental-balloon/virtio-backend`, whereas the experimental controller queried the PCI wrapper. The wrapper forwards standard `guest-stats` but does not forward this new property. With the correct path, QMP returned `false` without `OMARCHY_QEMU_BALLOON_DECOMMIT=1` and `true` with that setting and WHPX. The controller path and unit test were corrected.

In a disposable `-snapshot` boot of the inactive acceptance disk, the guest reached SSH and the balloon reached the 2 GiB target from 4 GiB. Regrowth timed out after 70 seconds at 3,644,850,176 bytes, while QEMU wrote 52,700,887 bytes of `Couldn't MADV_WILLNEED on balloon deflate: Invalid argument` warnings. The guest workload completed at least 36 SHA-256 integrity rounds without mismatch before the failed test terminated its disposable QEMU process. `QEMU_MADV_WILLNEED` has no Windows implementation in this build; the warning runs once per returned 4 KiB page and slows the regrowth path. Experimental patch 0015 skips that unsupported hint on Windows. The normal installed VM was not touched.

## Corrected Windows deflation build

The [third isolated build](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36252837934) passed CI and includes patch 0015. The workflow artifact SHA-256 was `b4d668abc33828f94fe053b14915ae715a156102cfa1601584152bbc28b8e195`; the portable ZIP SHA-256 was `39cc2a6a5016c7fb3eecb4e1ce0993edc5b4f5b38476b184e24d188bb924199a`, matching its bundled `SHA256SUMS`. The separately built Windows QEMU executable SHA-256 was `43160f86cbf28a67df6f529b104dd03f63a37ca5d3d2a9148f11358fbc559b0a`. Only this physically checked executable is accepted by the experimental controller's exact hash guard.

On the Windows 11 test host, QMP returned `false` for the backend attestation without opt-in and `true` with WHPX and `OMARCHY_QEMU_BALLOON_DECOMMIT=1`. In a disposable 4 GiB `-snapshot` guest, QMP guest statistics were current and reported 3,145 MiB available before pressure. A Python process kept 384 MiB of written data and repeatedly checked SHA-256 hashes during two complete 4 GiB → 2 GiB → 4 GiB cycles. Both shrinks reached exactly 2,147,483,648 bytes and both regrowth requests reached exactly 4,294,967,296 bytes. All 120 integrity rounds completed with zero mismatches.

Before the first shrink, QEMU's working set was 2,282.1 MiB and Windows available physical memory was 17,659.5 MiB. At the 2 GiB target, they were 1,650.5 MiB and 18,339.9 MiB respectively: QEMU's resident set fell 631.6 MiB while the host available-memory sample rose 680.4 MiB. QEMU private bytes remained about 4,213.1 MiB. After regrowth, QEMU's working set stayed low until the guest touched more pages, which is consistent with demand-zero recommit rather than an immediate refill. The second shrink/grow also completed. Host memory samples can move due to other processes and are not a guaranteed reclaim amount.

The QEMU stderr log was 128 bytes, containing one unrelated interrupt-vector warning and no per-page `MADV_WILLNEED` warnings. The disposable VM shut down normally. The installed Omarchy QEMU process was the only remaining QEMU process, and its guest still answered SSH. This verifies the patched runtime on one Windows host and a 384 MiB integrity workload; it does not validate sustained gaming or graphics pressure, nor does it enable automatic RAM changes in the normal launcher.

## Automatic controller with bounded host pressure

The opt-in Windows physical test in `app/experimental_balloon_windows_physical_test.go` used the same pinned QEMU executable (`43160f86cbf28a67df6f529b104dd03f63a37ca5d3d2a9148f11358fbc559b0a`), the inactive acceptance disk with QEMU `-snapshot`, and a loopback-only QMP and SSH connection. The installed runtime and VM were not started or changed for these tests. The controller sampled actual Windows available physical memory and current QMP balloon/guest statistics. A 768 MiB touched `VirtualAlloc` region supplied bounded host pressure. Test-only policy thresholds were injected in memory to place the threshold near the host's current 47 GiB of available RAM; the production headroom was 8,164 MiB and would not have triggered any change under this host load. The test-only guest floor was 3,584 MiB, limiting the shrink to 512 MiB.

| Final passing run (`omarchy-balloon-auto-20260926-4`) | Measurement |
| --- | ---: |
| Windows available before / under pressure / after automatic shrink | 47,475 / 46,699 / 46,869 MiB |
| Windows available after pressure release / after automatic regrowth | 47,660 / 47,897 MiB |
| QMP actual before / after shrink / after regrowth | 4,096 / 3,584 / 4,096 MiB |
| QEMU working set before / after shrink / after regrowth | 2,259.8 / 2,247.1 / 2,274.5 MiB |
| Guest integrity workload | 120 of 120 JSON rounds, zero mismatches; 384 MiB of written data |
| End state | ACPI shutdown completed; no QEMU process remained |

The checked run's [measurements](balloon-auto-20260926-result.json) and [raw guest integrity rows](balloon-auto-20260926-integrity.jsonl) are retained in this repository. Their SHA-256 digests are `05df84298367888bf02f3fceae7343d8483de3efb86f753e2dd3039b2b684fbb` and `c9f64fa2c59b153c68adbd2d329d99a3b57a0c2562414f166ba346dca9b0295`, respectively.

The host available-memory sample fell by 776 MiB when the 768 MiB host region was touched, then rose by 961 MiB from that point after the region was released and the guest regrew. The automatic 512 MiB balloon shrink itself changed QEMU's sampled working set by only 12.8 MiB. Most of those guest pages may not have been resident, and unrelated Windows activity also changes available-memory samples. This run verifies automatic policy response and guest integrity, **not a measured 512 MiB host RAM return**. The separate 4→2 GiB manual QMP run above is the physical host RAM-return evidence (631.6 MiB QEMU working-set drop and 680.4 MiB Windows available-memory rise).

Three preceding attempts are relevant to reliability. The first connected a Windows Unix QMP socket after guest SSH became ready and timed out waiting for the QMP greeting. The second used loopback TCP but also connected after SSH and timed out at the same step. Neither attempted a balloon change or allocated host pressure. The third connected a single loopback TCP QMP client as the disposable QEMU started; automatic 4→3.5→4 GiB changes completed, but the harness rejected the integrity output. Its strict JSON parser consumed combined SSH stdout and diagnostic stderr. The output was not saved before cleanup, so the cause of that failure remains unknown. The fourth run separated SSH stderr, saved raw guest output, confirmed rounds 0–119 were all `okay: true`, and shut down cleanly. Early QMP connection has been observed to wedge a different WHPX launch path, so this disposable test result does not clear QMP timing for normal startup. No automatic balloon controller is wired into normal launch.
