# Experimental native foreground QMP runtime

This recipe copies the pinned production runtime source and patch list, then
adds the independently pinned `0013` and `0014` QEMU patches. It produces separate
`winq-emu-alpha10-native-foreground-v2-experimental-*.zip` archives. The normal
`runtime-build/sources.lock.json`, release runtime, and installed Omarchy are
unchanged.

The Windows SDL QMP command `__omarchy_native-foreground-handoff-v2` delegates
`AllowSetForegroundWindow` permission to QEMU's bound launcher parent only after rechecking
the foreground SDL display, the exact native HWND/PID/process creation time,
the Omarchy grant marker, a short Windows uptime expiry, the launcher's
held process identity and tray HWND, the active desktop and session. It does
not move or focus a window. This privileged command
requires a private host QMP socket;
QAPI alone cannot identify its caller. The launcher experiment must validate
the socket ACL and each grant and lease before requesting it.

The launcher experiment allows this request only after a prior committed
native-layout lease and within a 250 ms budget for the current layout. It
attempts once for a visible tile state. A hidden-to-visible transition or
changed tile can retry only after a five-second cooldown, including if guest
error recovery produced the hidden state. The first layout and expired leases
use ordinary placement without the QMP handoff.
If an initial visible layout cannot be placed, it cannot acquire a committed
lease and this experiment will not retry the privileged command. A later
successfully committed hidden layout can establish the lease for a subsequent
visible transition. This is an experimental recovery limit.

Build on MSYS2 UCRT64 with the normal runtime build dependencies:

```sh
bash runtime-build/experimental/native-foreground/build.sh runtime-output-native-foreground-experimental
```

CI compiles QEMU, checks the generated QAPI, and runs a headless QMP smoke
that confirms the command exists and rejects `ASFW_ANY`. This is a source and
build experiment. It does not prove Windows z order, input behavior, or the
candidate desktop experience; those require a separate physical test with a
disposable VM. Do not use the experimental runtime for normal launches.
