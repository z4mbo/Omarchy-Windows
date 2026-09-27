# Existing-install upgrade validation — September 26, 2026

## First automatic run

[CI #21](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36263958848)
tested published commit `b02af134ac7783e0d9774fcacf6f1e2c68c1e50d`, whose
source tree matches local `e6c1cd0b345a5d5745b134ab0b85b17a615674e8`.
The Windows and Linux launcher tests, all 29 release-helper tests, guest
contract/parity checks, full revision-35 image build, and fresh desktop boot
passed. The new existing-install upgrade job **failed**.

The job authenticated the public `v0.0.20-preview` baseline against fixed
manifest SHA256 `bbdf1d478dc0a15fd105cde47057ab85190510b59742ffabf8da1a94117d2d49`.
Its compressed and decompressed disk hashes matched. The same-run candidate
boot inputs matched manifest
`f55968e527015bd2546799f0e93532f7d9d2b8efb9cd2671a3fbd35017e911ea`.
A disposable 24 GiB QCOW2 overlay held all test writes.

- The older image booted and seeded document/configuration preservation hashes.
- The candidate booted that disk with revision 35 and working native helper
  imports and occlusion negotiation checks.
- TUN, the camera module, module metadata, and completion markers passed.
- The updater respected the test-owned package lock. After that fixture was
  removed, the ordinary Omarchy update reached pacman's file-conflict check.
- Pacman rejected three existing paths under
  `/usr/lib/modules/7.2.7-arch1-1/vdso/`: `vdso32.so`, `vdso64.so`, and
  `vdsox32.so`. The conflicting package was `linux-headers` 7.2.7; the installed
  `linux` package remained held at 7.2.6 for the external-kernel architecture.
- No packages were upgraded in that failed transaction. The later reboot and
  backward-image phases did not run, so they have no passing evidence here.

The module archive excluded `build`, `source`, and `vmlinuz`, but included
these header-owned files. Extracting it onto the older disk created the
conflict before the package transaction. This is a real upgrade compatibility
defect caught by the new test, not a reason to bypass the conflict check.

Evidence artifact `guest-upgrade-evidence`, ID `10912499869`, contains the two
serial logs. Its ZIP SHA256 is
`814c55eb38e561c797f2d0b25f0f8f3b4367b7b10a64cfc0b7f33ef01d419902`.
The artifact expires after 14 days. A local authenticated copy was inspected.

## Revision 36 correction and repeat test

Guest patch 0088 excludes header-owned vDSO files from future module delivery.
Its boot-time repair compares the exact known files with authenticated image
hashes and queries package ownership. It preflights every file before removal
and preserves modified files, symlinks, and ambiguous package state. The update
repository now requires successful completion of that repair. Normal package
checks and complete kernel/network/camera module delivery remain enabled.

The patch was generated from guest source commit
`19a4c7d13ac173bbad36f92dff149106d8f927cf` and reverse-apply checked. It adds
seven migration regression cases and extends the archive exclusion test.
The real upgrade fixture also checks pacman's unowned-file diagnostic, which
the repair uses to distinguish unowned files from a failed ownership query.

The corrected image must pass the same complete five-boot test before upgrade
acceptance. Later run results are recorded in
[PR #1](https://github.com/z4mbo/Omarchy-Windows/pull/1). Even a passing run
does not establish guest package rollback, physical Windows app behavior,
game performance, or support for unknown future upstream changes.
