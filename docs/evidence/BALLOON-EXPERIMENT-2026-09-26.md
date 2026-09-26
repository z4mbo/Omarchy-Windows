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
