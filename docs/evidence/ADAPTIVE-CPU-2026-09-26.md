# Adaptive CPU scheduling on Windows — 2026-09-26

Balanced and Maximum Performance now adjust the QEMU process's scheduling
priority while it runs. This changes scheduling preference; it does not add
vCPUs, impose a CPU quota, return RAM, or control GPU use.

## Policy

The launcher samples Windows CPU use and the foreground process every two
seconds. With another Windows application active, at least 70% aggregate CPU
use sustained for six seconds changes QEMU from Normal to Below Normal.
Returning to Omarchy restores Normal at the next sample. At most 50% CPU use
sustained for ten seconds also restores Normal. Unknown readings restore
Normal. The thresholds avoid reacting to brief load spikes.

Manual resource mode does not start this controller. Hosts with more than
64 logical processors are excluded because the current CPU sampler does not
cover all processor groups. A QEMU process that starts at another priority is
left alone. A detected external priority change stops automatic adjustments.
The controller holds a process handle, so later operations stay attached to
that process lifetime. Cleanup restores Normal only if its own priority
setting is still present.

## Verification

On the Windows 11 host, `go test ./... -run TestAdaptiveCPU -count=1 -v`
passed, followed by the complete Go suite (`go test ./...`, 30.2 seconds for
the launcher package). The policy tests cover pressure and recovery delays,
hysteresis, foreground changes, and invalid samples.

The native Windows test used a hidden disposable child test process. Actual
Win32 calls changed its priority from Normal to Below Normal and back, checked
cleanup restoration, preserved an external priority override, and skipped a
process already running at Below Normal. Test time and CPU samples were
controlled; this was not a real foreground game workload. No normal Omarchy
VM or other application had its priority changed by the test.

## Remaining acceptance

A simultaneous Windows game and guest workload has not yet measured guest
throughput, Windows frame times, or the behavior of WHPX vCPU scheduling under
this policy. No FPS improvement or near-native performance is claimed.

Microsoft documents the process priority API in
[SetPriorityClass](https://learn.microsoft.com/en-us/windows/win32/api/processthreadsapi/nf-processthreadsapi-setpriorityclass).
Windows client Hyper-V uses the root scheduler, which delegates scheduling to
Windows; see [Hyper-V scheduler types](https://learn.microsoft.com/en-us/windows-server/virtualization/hyper-v/manage/manage-hyper-v-scheduler-types).
