#!/usr/bin/env python3
"""Validate the launcher's independently pinned release manifest."""

from __future__ import annotations

import argparse
import hashlib
import re
import urllib.error
import urllib.request
from pathlib import Path


TAG_RE = re.compile(r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-preview)?$")
SHA_RE = re.compile(r"^[0-9a-f]{64}$")
REQUIRED = {
    "build-spec.json",
    "guest-manifest.json",
    "initramfs-linux.img",
    "rootfs.ext4",
    "rootfs.ext4.zst",
    "vmlinuz-linux",
    "winq-emu-alpha10-portable.zip",
}
INHERITED_UPDATE_KEY = "f1edc8c2fc8fc8a7a108832eb93a9d9f2f8c07c5547fc4e4cb805c3b1615c9cd"
UPSTREAM_BOOTSTRAP_URL = "https://github.com/omacom/try-omarchy-windows/releases/download/v0.0.20-preview"
UPSTREAM_BOOTSTRAP_DIGEST = "bbdf1d478dc0a15fd105cde47057ab85190510b59742ffabf8da1a94117d2d49"


def source_value(source: str, name: str) -> str:
    match = re.search(rf'\b{name}\s*=\s*"([^"]+)"', source)
    if not match:
        raise SystemExit(f"could not find {name} in app/manifest.go")
    return match.group(1)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("tag")
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--repository", default="z4mbo/Omarchy-Windows")
    parser.add_argument(
        "--allow-upstream-bootstrap",
        action="store_true",
        help="accept only the existing v0.0.20 upstream pin before the first fork release",
    )
    parser.add_argument(
        "--require-independent-key",
        action="store_true",
        help="reject the inherited upstream signing key when preparing a fork release",
    )
    parser.add_argument(
        "--require-public",
        action="store_true",
        help="also fetch the pinned release's SHA256SUMS over the network and "
        "confirm it is publicly reachable and matches the embedded fixture. "
        "Skip this during the release workflow's own pre-publish phases, "
        "where the release is still a draft by design.",
    )
    args = parser.parse_args()

    if not TAG_RE.fullmatch(args.tag):
        raise SystemExit(f"invalid release tag: {args.tag}")

    source = (args.root / "app/manifest.go").read_text(encoding="utf-8")
    release_url = source_value(source, "defaultReleaseURL")
    expected_digest = source_value(source, "defaultSumsSHA256")
    is_upstream_bootstrap = (
        args.allow_upstream_bootstrap
        and args.tag == "v0.0.20-preview"
        and release_url == UPSTREAM_BOOTSTRAP_URL
        and expected_digest == UPSTREAM_BOOTSTRAP_DIGEST
    )
    if is_upstream_bootstrap:
        args.repository = "omacom/try-omarchy-windows"

    update_source = (args.root / "app/update.go").read_text(encoding="utf-8")
    current_version = source_value(update_source, "currentVersion")
    if current_version != args.tag:
        raise SystemExit(f"currentVersion is {current_version}, expected {args.tag}")
    update_key = source_value(update_source, "updatePublicKeyHex")
    signer_source = (args.root / "app/cmd/sign-update/main.go").read_text(encoding="utf-8")
    signer_key = source_value(signer_source, "expectedPublicKeyHex")
    if update_key != signer_key or not SHA_RE.fullmatch(update_key):
        raise SystemExit("update signer key does not match the launcher trust root")
    require_independent_key = args.require_independent_key and not is_upstream_bootstrap
    if require_independent_key and update_key == INHERITED_UPDATE_KEY:
        raise SystemExit("fork release requires its own Ed25519 update signing key")
    if require_independent_key:
        update_url = source_value(update_source, "defaultUpdateURL")
        expected_update_url = f"https://github.com/{args.repository}/releases/latest/download/update-v2.json"
        if update_url != expected_update_url:
            raise SystemExit(f"defaultUpdateURL is {update_url}, expected {expected_update_url}")
        release_base = source_value(update_source, "forkReleaseBase")
        expected_base = f"https://github.com/{args.repository}/releases/download/"
        if release_base != expected_base:
            raise SystemExit(f"forkReleaseBase is {release_base}, expected {expected_base}")
    expected_url = f"https://github.com/{args.repository}/releases/download/{args.tag}"
    if release_url != expected_url:
        raise SystemExit(f"defaultReleaseURL is {release_url}, expected {expected_url}")

    if not SHA_RE.fullmatch(expected_digest):
        raise SystemExit("defaultSumsSHA256 is invalid")

    embed = re.search(r"//go:embed\s+([^\s]+)", source)
    if not embed:
        raise SystemExit("could not find embedded manifest fixture")
    fixture = args.root / "app" / embed.group(1)
    if fixture.name != f"SHA256SUMS.{args.tag}":
        raise SystemExit(f"embedded fixture name does not match {args.tag}")
    data = fixture.read_bytes()
    actual_digest = hashlib.sha256(data).hexdigest()
    if actual_digest != expected_digest:
        raise SystemExit(
            f"embedded manifest digest is {actual_digest}, expected {expected_digest}"
        )

    entries: dict[str, str] = {}
    for line_number, line in enumerate(data.decode("utf-8").splitlines(), 1):
        match = re.fullmatch(r"([0-9a-f]{64})  ([^/\\\s]+)", line)
        if not match:
            raise SystemExit(f"invalid manifest line {line_number}")
        digest, name = match.groups()
        if name in entries:
            raise SystemExit(f"duplicate manifest entry: {name}")
        entries[name] = digest
    missing = sorted(REQUIRED - entries.keys())
    if missing:
        raise SystemExit(f"manifest is missing: {', '.join(missing)}")

    if args.require_public:
        sums_url = f"{release_url}/SHA256SUMS"
        try:
            with urllib.request.urlopen(sums_url, timeout=30) as response:
                live_data = response.read()
        except urllib.error.HTTPError as error:
            raise SystemExit(
                f"pinned release is not publicly reachable: {sums_url} returned {error.code}"
            ) from error
        except urllib.error.URLError as error:
            raise SystemExit(f"could not reach pinned release {sums_url}: {error.reason}") from error
        live_digest = hashlib.sha256(live_data).hexdigest()
        if live_digest != expected_digest:
            raise SystemExit(
                f"live manifest at {sums_url} is {live_digest}, expected {expected_digest}"
            )

    print(f"ok - {args.tag} pins authenticated manifest {actual_digest}")


if __name__ == "__main__":
    main()
