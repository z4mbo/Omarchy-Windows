# Native mode installation and update gates — 2026-09-26

This is a source audit of branch `codex/native-windows-apps`, not an installed
product acceptance. No VM, Windows desktop, installed disk, or system settings
were changed for this audit.

## What the source can package today

- Guest patch `0086-Add-native-Windows-layout-proxies.patch` raises the image
  integration revision from 33 to 34. It adds the native GTK proxy controller,
  layout translator, presentation negotiation, and guest contract tests to
  factory images. Its `finalize-rootfs.sh` compatibility list carries the new
  files into persistent guest disks when the external image is updated.
- Patch 0087 raises the integration revision to 35 and adds optional negotiated
  tile clipping for overlapping floating Linux clients. This extends protocol 1
  without requiring the extension from an older host or guest.
- The release helper checks out the exact guest source commit in
  `guest-build/source.lock.json`, applies every numbered patch, runs guest
  contract tests, builds the full image, and boots a disposable KVM desktop
  during the automatic CI image job. The CI job retains a candidate artifact for
  seven days; that artifact is not a signed public release.
- `scripts/windows-apps/install-in-guest.sh` is a manual development path. It
  copies user scripts and requires one `sudo` transaction for the token unit.
  The image patch is the route to the requested one-app installation. The
  manual script should not be presented as the final user setup.
- The host's `OMARCHY_WINDOWS_PRESENTATION=native` switch is opt-in. Protocol 1
  negotiation selects the native guest controller; an older host's absent
  presentation endpoint selects capture. The host verifies grants and bounds
  layouts. This does not prove the native desktop interaction physically.

The new revision-34 smoke fact checks that the booted image has an executable
`omarchy-windows-native` and that its `--help` path imports the packaged layout
and protocol modules without opening a window or using a bridge token.
Revision 33 and older smoke expectations stay unchanged. This checks delivery,
not a working native tile.

## Gates before a safe one-app candidate

1. **Physical native UI proof.** In an isolated graphical candidate, show a
   Windows app and Linux app side by side; verify keyboard/mouse, workspace
   switch and return, fullscreen game transitions, popups, scale, monitor
   movement, and original host window restoration after revoke, crash, and
   lease expiry. Compare frame time, image quality, CPU load, and input latency
   against the same standalone Windows workload. Source and unit tests alone
   cannot establish the requested behavior.
2. **Version 35 image and old-disk upgrade.** The image build and automated
   desktop boot passed in CI #20 below. The new automatic upgrade gate uses the
   authenticated public `v0.0.20-preview` actual-user baseline and a disposable
   QCOW2 overlay. It is awaiting its first CI result. Check native files and
   service, user-file hashes, edited system configuration, installed packages,
   backward-image boot, return to candidate, and repeated update. The current
   `docs/GUEST-UPGRADES.md` validation predates native revision 34.
3. **Host/guest compatibility at update boundaries.** `app/manifest.go` still
   defaults to the older `v0.0.20-preview` image; the launcher has no guest
   integration revision in `buildSpec`. A newer native host can therefore be
   opted in against an older disk. The host now retains capture until the first
   authenticated valid native layout, with mixed-version and in-flight-input
   tests. Native activation remains sticky for that boot. This preserves the
   older guest path but is not a managed upgrade experience. Add an authenticated
   image capability/revision field or a guest-ready capability response, and
   test revision-33 host/guest combinations before enabling native mode by
   default. Keep protocol 1 explicit and reject unknown future protocols until
   both sides implement them.
4. **Future Omarchy updates.** Build each upstream update from locked source
   and signed packages, bump the compatibility revision when an integration
   overlay changes, verify fresh image plus old-disk migration, and preserve
   user files and user-owned configuration. `docs/GUEST-UPGRADES.md` describes
   pacman backup/`.pacnew` handling and the update repository. It explicitly
   says package updating is not a rollback of the installed desktop. Keep a
   stopped-VM backup before upgrade acceptance; exercise launcher payload
   rollback separately from a guest package rollback.
5. **Linux resource behavior.** The normal launcher selects vCPU count and RAM
   at launch, then adjusts the QEMU process scheduling priority under sustained
   Windows CPU load. It does not change vCPU count or RAM live. The verified
   experimental balloon controller and private QMP topology are not wired to
   normal launch. A WSL/CUDA Blender render and Waypipe registration were
   isolated experiments, not a bundled interactive GPU graphics path. Validate
   the interactive GPU desktop, games, render workflows, host gaming contention,
   live RAM recovery, and sparse disk growth/reclaim before claiming native-like
   resource sharing.
6. **Display and installation lifecycle.** Verify first-run GUI choices,
   hypervisor setup, shortcuts, token bridge, all monitor resolutions/refresh
   rates and live changes, signed launcher publishing, update/rollback, and
   recovery from interrupted setup. Preserve the existing personalized disk
   and settings until a complete candidate passes; only then migrate or replace
   them with a verified backup and restoration path.

## Reproducible CI dispatch

`.github/workflows/ci.yml` now automatically builds and boots a candidate when
a pull request or push changes guest patches or image build/test inputs. It
also retains `workflow_dispatch` with `build_guest` defaulting to `false`; set
that input to `true` for an explicit rebuild. The existing GitHub
Actions **CI → Run workflow** control can dispatch it on
`codex/native-windows-apps` using the signed-in user's account, after the exact
candidate commit is pushed. Record the selected ref and resulting run SHA so a
later push cannot be mistaken for the built image. Do not dispatch a duplicate
while a candidate job for the same SHA is queued or running.

At this audit, `gh` was unavailable on the Windows PATH and the connected
GitHub tools exposed no workflow-dispatch operation. The remote commit
`e730bdd16a59b41da5a974d8ad60a54176802e18` and local commit
`239c727cf43b2ce54ac0dff7884bca8b0479f807` have the **same Git tree**,
`fb7bd4b8a00378444f988be6fad49e87f982c849`: the remote already contains
the revision-34 patch. Their commit hashes differ because the remote was
published with a different parent; unequal commit hashes did not mean the
source was behind. [CI #19](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36261865587)
passed its launcher, Windows launcher, and guest contract jobs on that remote
source. Its full guest image candidate job was skipped, as expected for a pull
request run without `build_guest=true`. The new revision-34 smoke gate in local
commit `17bb7de` is **not yet published** and was not part of CI #19. No
workflow was dispatched for this audit. There is no reason to obtain or print
GitHub credentials.

The separate release workflow requires the default branch, a new tag, a
protected release environment, an independent signing key, and publication
checks. Its source comments still flag the inherited development update key;
publishing a signed one-app release remains a separate gate.

## Revision 35 full-image evidence

[CI #20](https://github.com/z4mbo/Omarchy-Windows/actions/runs/36263005169)
completed successfully for published head
`1f3aad0ebef7caa0776393f71ce0490f42dea4e9` (PR merge checkout
`e9c3fc96c2bdda881ba2728c52f202ea7ebd6350`). All source jobs passed, and the
locked image built and booted under Linux KVM. Serial facts confirm revision
35, native helper imports, one Hyprland output, a visible file-transfer window,
matching kernel modules, and a clean/unlocked package database.

Artifact `guest-candidate`, ID `10912418718`, has archive SHA256
`72fe9b0df39f7b7a977eb3fda822b1dcc5bfe34f93dfa6a45184c14e9c87c82c`.
It expires October 3, 2026. This artifact digest identifies the CI archive,
not the internal release manifest. This is fresh-image evidence, not physical
Windows interaction, native app performance, or old-disk upgrade proof.

The next CI revision adds a packaged-source parity check, an automatic
older-install upgrade test with authenticated inputs, and a required upgrade
job before release publication. Those new checks remain pending until their
own exact-source run succeeds.
