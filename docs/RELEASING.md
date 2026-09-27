# Releasing Omarchy for Windows

Releases use a two-phase GitHub Actions workflow so the signed launcher can pin
the guest manifest produced for that same release without rewriting source code
inside CI.

## One-time setup

The `release` GitHub environment must be restricted to the `master` branch of
`z4mbo/Omarchy-Windows`. Its Azure application needs a GitHub OIDC federated
credential for **this repository**. Credentials and environment settings from
the upstream repository do not transfer to a fork.

The environment defines these variables:

- `AZURE_CLIENT_ID`
- `AZURE_TENANT_ID`
- `AZURE_SUBSCRIPTION_ID`
- `AZURE_SIGNING_ENDPOINT`
- `AZURE_SIGNING_ACCOUNT`
- `AZURE_SIGNING_PROFILE`

The owner must generate a **new** Ed25519 update key pair for this fork. Put its
public key in `updatePublicKeyHex` in `app/update.go` and
`expectedPublicKeyHex` in `app/cmd/sign-update/main.go`, and set the protected
`UPDATE_SIGNING_KEY` secret to the base64-encoded PKCS#8 private key. The
release validation rejects the inherited upstream public key. The signing
command rejects a private key that does not match the public key in source.
Never store the private key in the repository. Until the new key, Azure OIDC
credential, and Artifact Signing variables are configured, there is no signed
one-click fork release.

The Azure application needs the `Artifact Signing Certificate Profile Signer`
role on the signing account. The workflow itself requests only `id-token: write`
and `contents: write` in the protected publish job.

The optional `signing-check` phase builds, signs, and verifies the current
launcher and signed update metadata without creating or modifying a release.
It requires the matching source pin and includes `test-update.json` in the
artifact for verification. Use it after changing the OIDC or signing configuration.

## Prepare the guest

1. Add the new version section to `CHANGELOG.md` and push it to `master`.
2. Run the `Release` workflow with phase `prepare` and the new preview tag.
3. The workflow applies the locked guest patches, runs the guest contract tests,
   rebuilds the image, boots the instant account and one-display desktop,
   checks the Windows Apps integration and per-boot token, authenticates every
   artifact, and creates a draft release.
4. Download the draft `SHA256SUMS`, add it under `app/testdata`, and update
   `defaultReleaseURL`, `defaultSumsSHA256`, and the embedded fixture name in
   `app/manifest.go`. Update `currentVersion` in `app/update.go` and the numeric and text versions
   in `app/versioninfo.rc` to the same tag. Regenerate
   `app/rsrc_windows_amd64.syso` using the commands in `versioninfo.rc`.
5. Run `scripts/release/validate-pin.py TAG --require-independent-key`, commit,
   and push the pin. The launcher must point at the new release in
   `z4mbo/Omarchy-Windows`; the inherited v0.0.20 upstream pin is accepted only
   as a temporary CI bootstrap before the first fork release.

The pinned tag must be on `master` before `publish` runs: the workflow's guard
requires `refs/heads/master`, and the publish phase verifies that the source pin
matches the draft release manifest. This has one consequence worth expecting:
CI also runs `validate-pin.py --require-public` on every push to `master`, so
while the release is still a draft the "Validate current release pin" step fails
on `master`. That failure is correct, because a fresh build from `master` genuinely
cannot download the pinned assets yet. It clears as soon as `publish` makes the
release public. Do not merge a pin bump to an unpublished release through a pull
request; land it as the deliberate pre-publish step and publish promptly.

## Test the draft on physical Windows

A GitHub draft's assets require authentication, while the launcher downloads
without a GitHub token. Do not point the launcher directly at the draft URL.
After the manifest pin is pushed:

1. Run the `signing-check` phase on the pinned commit and download its signed
   launcher artifact.
2. Download every draft asset into one folder with an authenticated browser or
   `gh release download TAG --dir candidate-assets`.
3. Serve that folder over loopback on the test PC. For example, from the asset
   folder run `py -m http.server 18080 --bind 127.0.0.1`.
4. Close Try Omarchy and copy `%LOCALAPPDATA%\TryOmarchy` to a separate test
   directory. Never use the only copy of a real guest for candidate testing.
5. Start the signed candidate with the copied data directory and the local
   payload:

   ```powershell
   .\TryOmarchy.exe `
     -dir C:\TryOmarchyCandidate `
     -release http://127.0.0.1:18080 `
     -sums-sha256 SHA256SUMS_DIGEST `
     -runtime-release http://127.0.0.1:18080 `
     -runtime-sums-sha256 SHA256SUMS_DIGEST `
     -no-update
   ```

Confirm that the existing desktop and files survive, the new external kernel
boots, reboot and poweroff work, and a second launch does not repeat the guest
compatibility repair. For the rollback check, stop the first candidate boot
before userspace reports ready, then start the candidate again. The copied
install must restore its previous guest and runtime without downloading the
failed payload again.

Keep the release as a draft until the GPU, idle CPU, audio, input, resize, and
fullscreen checks in `docs/RUNTIME-VALIDATION.md` pass on physical hardware.

The guest builder base is fixed in `guest-build/source.lock.json`. The runtime
build inputs are fixed in `runtime-build/sources.lock.json`, and the Runtime
workflow produces matching portable and source archives with licenses,
provenance, and per-file hashes. Both archives used by a production release
stay fixed in `guest-build/runtime.lock.json`; update that lock only after the
new runtime passes `docs/RUNTIME-VALIDATION.md`.

## Publish

Run the `Release` workflow again with phase `publish` and the same tag. It:

- verifies that the source pin exactly matches the draft manifest;
- runs launcher tests and produces the optimized Windows build;
- signs through Azure Artifact Signing using GitHub OIDC;
- signs current update metadata with the protected Ed25519 update key;
- verifies Authenticode before upload;
- publishes without changing `Latest`;
- verifies the public tagged launcher, checksum, manifest, and guest URL in
  `z4mbo/Omarchy-Windows`;
- marks the release `Latest`, then verifies the fork's `latest/download` paths
  for the launcher and both signed update feed names.

If public verification fails, the release stays published but does not replace
the previous `Latest` release.

The updater accepts only a correctly signed manifest, a newer supported version,
the expected repository release URL, and matching SHA256 values. It stages the
launcher and payload directories atomically. Launcher, runtime, and guest
updates stay rollback-capable until the guest's userspace readiness service
reaches the launcher after networking starts, so QMP responding during a
kernel panic cannot commit a bad update. The writable disk is
preserved, and the updated initramfs installs the small matching launcher
integration onto disks created by older releases without replacing their OS or
user data.

## Moving from preview to stable

The fork's first release has its own signing key, update feed, and release URL.
It cannot update installations signed by upstream or make upstream's old
`update.json` feed point to this fork. Both `update.json` and `update-v2.json`
in fork releases contain the current fork metadata; fork launchers use the v2
feed. Keep both names available for tools that inspect release assets.

Before v1, test direct stable installation, updates from a published fork
preview, and forced rollback on physical Windows with a copied guest disk.
Stable installations never automatically switch to a preview. The workflow
prevents a later preview or older version from replacing a newer stable
Latest release.
