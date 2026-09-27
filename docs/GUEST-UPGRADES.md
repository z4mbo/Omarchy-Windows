# Updating an existing guest

## Fork acceptance requirement

For the Omarchy-for-Windows fork, future upstream updates are an explicit
product requirement. Each supported update must retain host/guest integration,
Windows app layout and input, graphics, display matching, resource controls,
and the user's files and settings. This requires continuing validation; it is
not a guarantee that an unknown future upstream change is already compatible.

Before automatic rollout, require fresh-image and older-disk boots, a complete
upgrade and reboot, mixed host/guest protocol versions, preserved configuration
fixtures, and recovery from a failed update. New optional features must be
capability-negotiated. Unrecognized compositor state must stop the integration
cleanly and release host windows rather than keep moving them from stale data.
Keep the last working image and a recoverable writable disk until acceptance
succeeds. The existing repository publisher below does not itself roll back
package transactions; that distinction remains a release gate.

See the [current install/update audit](evidence/NATIVE-INSTALL-GATES-2026-09-26.md)
for implemented checks and remaining work.

## Current guarded update path

The launcher can deliver a newer Omarchy runtime without replacing the writable
VM disk. The Windows app checks authenticated launcher releases on startup and
offers a manual check from **Settings > About and updates**. This does not yet
approve a guest package transaction.

The authenticated image carries the local package repository in its initramfs.
At boot, a service checks the payload hashes, copies new package archives, and
replaces the repository database atomically. It does not run pacman or modify its
lock. Old archives remain available to transactions that read the previous
repository. An older image cannot lower the repository's runtime version.

The installed **Update > Omarchy**, `omarchy update`, and update badge now use a
fork-owned gate. Channel changes also stop at that gate. It gives a visible
explanation and makes no package, AUR, or repository change. The update badge
does not advertise a live upstream package update that the product cannot yet
apply safely. Manual root package management remains possible, but lies outside
the managed update and recovery path. The launcher continues to supply the
external kernel and matching modules; the guest's linux package stays held.

The missing supported transaction requires a release-approved, frozen package
plan and a verified checkpoint of the **stopped** VM disk before package writes.
It then needs journaled success and failure handling, including a stopped-disk
rollback if the package transaction or readiness checks fail. The existing
launcher/payload rollback protects boot assets, not package changes already
written to the guest disk. Until that transaction is implemented and validated,
the gate reports that no managed guest package update is available. It does not
promise that unknown future upstream packages are compatible.

If repository publication fails, inspect:

```sh
journalctl -u try-omarchy-update-repository.service
```

Fix the reported cause, such as insufficient disk space, then retry:

```sh
sudo systemctl restart try-omarchy-update-repository.service
```

The publisher never removes `/var/lib/pacman/db.lck`. Keep a stopped-VM backup
before release-candidate testing. Repository publication is not an installed
desktop update or a rollback.

Older previews also copied the build account's ownership onto some system paths.
A boot service repairs only the paths supplied by the image overlays, without
following symlinks or recursing into personal files. It also normalizes unchanged
bundled icon filenames to the names expected by desktop files. This prevents
system and icon-cache hooks from failing during package updates.

## Validation

CI now follows each fresh guest image build with an older-disk upgrade job.
It downloads the public `v0.0.20-preview` baseline using a fixed independent
manifest pin, verifies both compressed and decompressed disks, and uses candidate
kernel/initramfs inputs from the same workflow run. Their manifest digest is
passed separately by the build job. The release publication job repeats this
test against the source-pinned release assets before signing and publishing.

The five boots share a disposable QCOW2 overlay; the factory baseline is hashed
before and after to detect mutation. Candidate boots also check the integration
revision, native helper imports, and supported occlusion negotiation. These
checks matter even when the baseline and candidate Omarchy package version is
unchanged. Logs are retained for 14 days. A backward-image boot tests the older
external kernel/initramfs against the upgraded disk; it does not undo installed
packages or prove package rollback. The first run caught a real
[kernel-header ownership conflict](evidence/UPGRADE-COMPATIBILITY-2026-09-26.md);
repeat-test results are recorded in [PR #1](https://github.com/z4mbo/Omarchy-Windows/pull/1).

Integration revision 36 excludes header-owned vDSO files from the module overlay
and repairs exact unowned copies left by older revisions. Repair checks image
hashes and ownership before deleting any file, and update-repository readiness
depends on its success. Changed files and uncertain ownership are preserved for
diagnosis. See the linked evidence for the original failure and repeat test.

On a Linux machine with KVM, use verified release artifacts and a newly built
candidate. Decompress the baseline `rootfs.ext4.zst` first. The work directory
must be new, and the test retains its disposable disk and logs for inspection:

```sh
python3 scripts/release/smoke-guest-upgrade.py \
  /path/to/older-release /path/to/candidate /path/to/new-test-directory \
  --disk-mode qcow2
```

The test provisions an older image, seeds preservation fixtures, upgrades it,
reboots it, boots it with the older external image, and returns to the candidate.
It checks package versions, user files, an edited system configuration, installed
packages, busy-lock handling, repair services, and repeated publication. It does
not modify either input image. Network access is required for the normal signed
Arch repository updates.

The default mode requires `qemu-img`; `--disk-mode raw` retains the earlier
sparse-copy test path. CI checks at least 35 GiB free on a separate runner before
fetching the baseline, after reclaiming that disposable runner's unused language
tool cache. For manual tests, size the available storage for the
verified baseline and the changes written during package updates. From
compatibility revision 40, its disposable Linux guest first proves the installed
Update/channel commands make no package or repository changes, then invokes the
retained pinned upstream updater by its explicit test-only path. The later
successful package upgrade validates compatibility of that candidate package
set; it does not validate a supported one-click Windows guest updater or
automatic recovery.


The guest contract suite tests corrupt and incomplete payloads, interrupted
publication, retry, repeated publication, downgrade rejection, archive conflicts,
and unsafe filesystem paths. Release validation must also boot a copy of a real
older image, perform the package upgrade, and reboot with preservation fixtures.
Windows launcher rollback and graphical acceptance remain separate release gates.

### Candidate validation, 2026-09-12

Baseline: the published `v0.0.14-preview` factory image, with its checksums
verified against the launcher's pinned checksum list. Candidate: guest patch
0046 on top of the Omarchy 4.0.3 work in #91, runtime `4.0.3-2` and compatibility
revision 14.

Passed on a disposable 24 GiB disk under QEMU/KVM:

- Provisioned the actual 4.0.2 image and seeded a document, user configuration,
  and modified system configuration with recorded checksums.
- Booted the candidate without changing the installed runtime first.
- Confirmed a test-owned pacman lock blocks an update and remains untouched.
- Ran the then-installed complete `omarchy-update -y` command successfully. Optional prompts
  timed out without being accepted. Package hooks reported no execution errors.
- Verified runtime `4.0.3-2`, media-tool dependencies, all previously explicit
  packages, and unchanged preservation fixtures. Repeated migrations succeeded.
- Rebooted the upgraded disk, booted it with the old external kernel/initramfs,
  and returned to the candidate. Each boot retained 4.0.3 and the fixtures.
- Confirmed repository publication can be repeated, system ownership is repaired,
  and the package database is unlocked after the update.

A separate fresh-image boot passed browser-policy repair and passwordless theme
policy checks, icon-cache generation, package availability, kernel-module
matching, and the readiness service. All 70 guest behavioral tests and 13 release
script tests passed, as did reconstruction from the complete guest patch series.

This is headless guest validation. It does not complete the Windows signing,
Hyper-V, interactive desktop, or launcher update/rollback acceptance gates.

## Package-lock interruption test

To investigate #90 independently of a launcher update, use the published release
artifacts verified against the launcher's pinned `SHA256SUMS`. Decompress the
factory image with `zstd -d --long=28 --sparse rootfs.ext4.zst -o rootfs.ext4`.
On a Linux host with KVM, run:

```sh
python3 scripts/release/smoke-package-recovery.py \
  /path/to/verified-release /path/to/new-evidence-directory
```

The runner copies the factory image to a new disposable 24 GiB disk and retains
that disk and four serial logs. Allow enough host space for the image and package
updates. Network access is needed for the disposable guest's source-pinned
upstream updater, invoked outside the supported menu path. It checks the initial
lock state, runs that test-only updater, then installs a local fixture
package whose pre-transaction hook pauses while pacman holds its real lock. A
competing transaction must fail without changing the lock. The test kills only
that fixture's systemd service, powers the VM off, and reboots. On the next boot
`try-omarchy-pacman-lock.service` removes the orphaned lock on its own and logs
the reason, so the fixture then installs normally with no manual lock deletion.
User-file hashes must match, and package-database diagnostics and their exit
status must remain identical to the post-update baseline. Existing database
errors are printed and retained, not treated as a clean integrity result.

The recovery only removes a lock it can prove orphaned: a regular file, no
pacman/alpm process running, no process holding it open, and an mtime older than
the current boot. An active transaction's lock is never touched, and anything
ambiguous is left in place and logged.

The fixture scripts still deliberately kill a package transaction; never run them
directly on a host or a valued guest. This covers a controlled interruption
before package writes, the orphaned-lock auto-recovery, and the following normal
transaction. It does not cover power loss during extraction, a partially
installed system update, Windows launcher rollback, or the original reporter's
unknown interruption. Passing it does not establish those other cases.

## Package-write interruption and offline recovery

The separate `smoke-package-write-recovery.py` runner uses the completed,
stopped upgrade-test disk and the authenticated candidate boot assets. CI and
release publication require this additional test after the five normal boots:

```sh
python3 scripts/release/smoke-package-write-recovery.py \
  /path/to/candidate /path/to/upgrade-evidence/persistent.qcow2 \
  /path/to/new-package-write-evidence --source-format qcow2
```

The runner creates a disposable child disk and installs version 1 of a local
fixture package. After a clean shutdown it creates an independent, flattened
disk backup and verifies that backup against the stopped source. A separate
child then upgrades the fixture to version 2. A test-only write interceptor
allows part of its payload to be written and flushed before stopping pacman.
The guest checks the stopped process, partial payload and held package lock
before announcing a unique marker. Only that marker allows the runner to kill
its own QEMU process abruptly.

The torn disk is inspected separately. Recovery boots a new child of the saved
backup and checks the original package contents, package database, document and
configuration hashes. A normal version-2 update and another reboot must then
succeed. Input disks, their backing files and the backup are checked for
unexpected changes. Serial logs and the recovery receipt are retained for
14 days; current run results are recorded in
[PR #1](https://github.com/z4mbo/Omarchy-Windows/pull/1).

This test exercises an actual package payload write and full-disk recovery on
Linux KVM. It does not establish recovery from every possible interruption,
automatic package rollback in the Windows product, or Windows snapshot UI
behavior. The package fixture and interceptor must only run in the disposable
test guest. The installed Omarchy disk is not an input to CI.
