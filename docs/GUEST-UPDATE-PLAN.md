# Frozen guest package plan, schema 1

This is a metadata contract for a future managed guest package transaction. The
launcher currently parses and authenticates the plan only in tests. It does not
download package archives for installation, make a checkpoint, run pacman, or
change the installed disk.

The release must include `guest-update-plan.json` and every named package archive
in `SHA256SUMS`. The signed `update-v2.json` pins that release manifest's SHA256;
the plan loader verifies the signature, manifest digest, plan digest, and each
package archive digest entry in that order. The fork must replace the inherited
development signing key before publishing a supported update.

Example shape (digest values abbreviated here; a real plan requires 64 lowercase
hex characters):

```json
{
  "schema": 1,
  "baseline": {
    "release": "v0.0.20-preview",
    "releaseManifestSHA256": "<64 lowercase hex characters>",
    "managedPackages": [
      { "name": "try-omarchy-runtime", "installedVersion": "4.0.3-6" }
    ]
  },
  "target": {
    "release": "v0.0.21-preview",
    "architecture": "x86_64",
    "kernelRelease": "7.2.7-arch1-1",
    "compatRevision": 41
  },
  "packages": [
    {
      "name": "try-omarchy-runtime",
      "version": "4.0.3-7",
      "architecture": "x86_64",
      "filename": "try-omarchy-runtime-4.0.3-7-x86_64.pkg.tar.zst",
      "sha256": "<64 lowercase hex characters>"
    }
  ]
}
```

There must be exactly one baseline precondition for every target package. A
precondition may instead use `"absent": true` to require that a newly managed
package is not installed. Unrelated user-installed packages are outside this
metadata; the later transaction must check for conflicts and preserve them.
The target release must match the signed update version, and the baseline
release must be older. The package filename is derived exactly from its name,
version without an optional epoch, architecture, and `.pkg.tar.zst` suffix.

The parser rejects unknown, non-ASCII, or duplicate JSON field names, nulls,
duplicate packages, missing or duplicate preconditions, paths, unsupported
architecture, invalid kernel or compatibility revision, and plans over 2 MiB or
4096 packages. A matching plan alone never authorizes package writes. A later
transaction still
needs installed receipt and pacman preflight, enough free space for an independent
stopped-disk checkpoint whose recovery can be verified, an exclusive lifecycle
lock, staged archive hash checks, a recovery journal, and post-reboot health checks.
