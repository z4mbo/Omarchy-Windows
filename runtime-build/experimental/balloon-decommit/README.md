# Windows guest RAM discard experiment

This recipe produces a **separate, unshipped QEMU runtime** from the pinned
WINQ-EMU source and the production patch set, then adds
`0001-windows-recommit-discarded-anonymous-ram.patch`. It does not change the
production source lock or installed runtime. The archives are named
`winq-emu-alpha10-balloon-experimental-*.zip` and include the experimental
patch in their source provenance.

Build from an MSYS2 UCRT64 shell with the packages required by
`runtime-build/packages.txt`:

```sh
bash runtime-build/experimental/balloon-decommit/build.sh runtime-output-balloon-experimental
```

The separate `runtime-balloon-experimental.yml` workflow runs the same build
on a Windows GitHub runner when these experimental files are pushed to a
`codex/**` branch. It uploads the archives after compiling QEMU, verifying
their source/provenance checksums, and running a lightweight QEMU memory smoke
test. It does not install the binary on the user's Windows machine.

On Windows, the lightweight API fixture can be run before a full build:

```sh
python runtime-build/experimental/balloon-decommit/test_win32_ram_discard.py
```

The patched runtime behaves exactly like the production runtime unless its
process environment contains `OMARCHY_QEMU_BALLOON_DECOMMIT=1`. Opting in
skips WHPX guest RAM pinning and discards QEMU-owned anonymous guest RAM via
`VirtualFree(MEM_DECOMMIT)` followed by same-address
`VirtualAlloc(MEM_COMMIT)`. File-backed, shared, and caller-owned blocks stay
on the existing path. Recommit failure stops QEMU because the guest RAM range
would otherwise be inaccessible. Use only with a disposable VM and backup.

This is a source experiment, **not** proof that WHPX releases physical RAM or
that a live guest remains stable. Before shipping, compile it, run the Windows
API fixture, and measure QEMU private/working memory plus guest integrity
across inflate/deflate cycles on a disposable VM. The installed Omarchy runtime
must remain unchanged until those checks pass.

Microsoft documents [decommit and physical storage release](https://learn.microsoft.com/en-us/windows/win32/api/memoryapi/nf-memoryapi-virtualfree),
[same-address recommit and zero-fill](https://learn.microsoft.com/en-us/windows/win32/api/memoryapi/nf-memoryapi-virtualalloc),
and [WHPX RAM pinning](https://learn.microsoft.com/en-us/virtualization/api/hypervisor-platform/funcs/whvadvisegparange).
