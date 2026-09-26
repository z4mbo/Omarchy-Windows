#!/usr/bin/env python3
"""Prepare verified v0.0.20 baseline and same-run candidate upgrade inputs.

This does not boot a VM. The candidate manifest digest must come independently
from the original build job or a source-pinned release fixture.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import tempfile
import urllib.request
from pathlib import Path


BASELINE_URL = "https://github.com/omacom/try-omarchy-windows/releases/download/v0.0.20-preview"
BASELINE_SUMS_SHA256 = "bbdf1d478dc0a15fd105cde47057ab85190510b59742ffabf8da1a94117d2d49"
BASELINE_FIXTURE = Path(__file__).resolve().parents[2] / "app/testdata/SHA256SUMS.v0.0.20-preview"
BASELINE_ASSETS = ("build-spec.json", "vmlinuz-linux", "initramfs-linux.img", "rootfs.ext4.zst")
CANDIDATE_ASSETS = ("build-spec.json", "vmlinuz-linux", "initramfs-linux.img")
SUM_LINE = re.compile(r"([0-9a-f]{64}) [ *]([^/\\\s]+)\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
MIB = 1 << 20
GIB = 1 << 30
MAX_MANIFEST = MIB
MAX_ROOTFS = 32 * GIB
MAX_ARCHIVE = 16 * GIB
MIN_FREE_RESERVE = 2 * GIB
FALLBACK_SMALL_ASSET_BUDGET = 512 * MIB


class PreparationError(ValueError):
    """An input is unauthenticated, incomplete, or too large for safe use."""


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(MIB), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_regular(path: Path, maximum: int) -> bytes:
    if path.is_symlink() or not path.is_file():
        raise PreparationError(f"Missing or unsafe regular file: {path}")
    if path.stat().st_size > maximum:
        raise PreparationError(f"File exceeds its size limit: {path}")
    data = path.read_bytes()
    if len(data) > maximum:
        raise PreparationError(f"File exceeds its size limit: {path}")
    return data


def parse_sums(data: bytes, expected_sha256: str) -> dict[str, str]:
    if not SHA256.fullmatch(expected_sha256):
        raise PreparationError("The independent SHA256SUMS digest is invalid")
    if len(data) > MAX_MANIFEST or hashlib.sha256(data).hexdigest() != expected_sha256:
        raise PreparationError("SHA256SUMS does not match its independent digest")
    try:
        lines = data.decode("utf-8").splitlines()
    except UnicodeDecodeError as exc:
        raise PreparationError("SHA256SUMS is not UTF-8") from exc
    entries: dict[str, str] = {}
    for number, line in enumerate(lines, 1):
        match = SUM_LINE.fullmatch(line)
        if not match:
            raise PreparationError(f"Invalid SHA256SUMS line {number}")
        digest, name = match.groups()
        if name in entries:
            raise PreparationError(f"Duplicate SHA256SUMS entry: {name}")
        entries[name] = digest
    if not entries:
        raise PreparationError("SHA256SUMS is empty")
    return entries


def require_names(entries: dict[str, str], names: tuple[str, ...]) -> None:
    missing = [name for name in names if name not in entries]
    if missing:
        raise PreparationError(f"SHA256SUMS lacks required entries: {', '.join(missing)}")


def guest_sizes(data: bytes, entries: dict[str, str]) -> dict[str, int]:
    if len(data) > MAX_MANIFEST:
        raise PreparationError("Authenticated guest-manifest.json is oversized")
    try:
        document = json.loads(data)
    except (ValueError, UnicodeDecodeError) as exc:
        raise PreparationError("Authenticated guest-manifest.json is invalid JSON") from exc
    if not isinstance(document, dict) or type(document.get("schemaVersion")) is not int or document["schemaVersion"] != 1:
        raise PreparationError("Unsupported guest-manifest.json schema")
    artifacts = document.get("artifacts")
    if not isinstance(artifacts, list):
        raise PreparationError("Invalid guest artifact size list")
    sizes: dict[str, int] = {}
    for item in artifacts:
        if not isinstance(item, dict):
            raise PreparationError("Invalid guest artifact entry")
        name, size, digest = item.get("path"), item.get("bytes"), item.get("sha256")
        if (not isinstance(name, str) or not name or "/" in name or "\\" in name
                or name in (".", "..") or type(size) is not int or size <= 0
                or size > MAX_ROOTFS or not isinstance(digest, str)
                or digest != entries.get(name) or name in sizes):
            raise PreparationError(f"Invalid or unauthenticated guest artifact size: {name}")
        sizes[name] = size
    for name, maximum in (("rootfs.ext4", MAX_ROOTFS), ("rootfs.ext4.zst", MAX_ARCHIVE)):
        if not 0 < sizes.get(name, 0) <= maximum:
            raise PreparationError(f"Missing or oversized guest artifact size: {name}")
    return sizes


def require_disk_budget(parent: Path, sizes: dict[str, int]) -> int:
    small = sum(sizes.get(name, FALLBACK_SMALL_ASSET_BUDGET) for name in BASELINE_ASSETS[:3])
    needed = sizes["rootfs.ext4"] + sizes["rootfs.ext4.zst"] + small + MIN_FREE_RESERVE
    available = shutil.disk_usage(parent).free
    if available < needed:
        raise PreparationError(f"Baseline needs {needed} free bytes including reserve; {available} available")
    return needed


def download_verified(url: str, destination: Path, digest: str,
                      expected_size: int | None, maximum: int) -> None:
    if not url.startswith(BASELINE_URL + "/") or destination.exists():
        raise PreparationError("Baseline source or destination is unsafe")
    if not 0 < maximum <= MAX_ROOTFS or (expected_size is not None and not 0 < expected_size <= maximum):
        raise PreparationError("Baseline asset has no safe expected size")
    temporary = destination.with_name(destination.name + ".part")
    hasher = hashlib.sha256()
    count = 0
    try:
        with urllib.request.urlopen(url, timeout=60) as response, temporary.open("xb") as output:
            if not response.geturl().startswith("https://"):
                raise PreparationError("Baseline asset redirected outside HTTPS")
            while chunk := response.read(MIB):
                count += len(chunk)
                if count > maximum:
                    raise PreparationError(f"Baseline asset exceeds its authenticated size: {destination.name}")
                hasher.update(chunk)
                output.write(chunk)
        if (expected_size is not None and count != expected_size) or hasher.hexdigest() != digest:
            raise PreparationError(f"Baseline asset size or SHA256 mismatch: {destination.name}")
        temporary.replace(destination)
    finally:
        temporary.unlink(missing_ok=True)


def runtime_version(spec: object) -> str:
    if not isinstance(spec, dict) or not isinstance(spec.get("upstream"), dict) or not isinstance(spec.get("runtime"), dict):
        raise PreparationError("build-spec.json lacks valid runtime boot fields")
    upstream = spec["upstream"]
    version = upstream.get("version")
    release = upstream.get("packageRelease", 1)
    cmdline = spec["runtime"].get("kernelCommandLine")
    if (not isinstance(version, str) or not version or type(release) is not int
            or release <= 0 or not isinstance(cmdline, str) or not cmdline):
        raise PreparationError("build-spec.json lacks valid runtime boot fields")
    return f"{version}-{release}"


def verify_candidate(directory: Path, independent_sums_sha256: str) -> dict:
    if not directory.is_dir() or directory.is_symlink():
        raise PreparationError("Candidate artifact directory is missing or unsafe")
    data = read_regular(directory / "SHA256SUMS", MAX_MANIFEST)
    entries = parse_sums(data, independent_sums_sha256)
    require_names(entries, CANDIDATE_ASSETS + ("rootfs.ext4", "rootfs.ext4.zst"))
    result = {}
    for name in CANDIDATE_ASSETS:
        path = directory / name
        if path.is_symlink() or not path.is_file() or path.stat().st_size > GIB:
            raise PreparationError(f"Candidate asset is missing or unsafe: {name}")
        if sha256_file(path) != entries[name]:
            raise PreparationError(f"Candidate asset SHA256 mismatch: {name}")
        result[name] = {"sha256": entries[name], "bytes": path.stat().st_size}
    if (directory / "rootfs.ext4").exists():
        raise PreparationError("Candidate artifact unexpectedly contains rootfs.ext4; this check covers only the three boot inputs")
    try:
        spec = json.loads(read_regular(directory / "build-spec.json", 2 * MIB))
    except (ValueError, UnicodeDecodeError) as exc:
        raise PreparationError("Candidate build-spec.json is invalid JSON") from exc
    return {"manifest_sha256": independent_sums_sha256, "assets": result,
            "runtime_version": runtime_version(spec),
            "skipped_manifest_entries": ["rootfs.ext4", "rootfs.ext4.zst"]}


def decompress_sparse(archive: Path, destination: Path, expected_size: int, digest: str) -> None:
    if destination.exists() or shutil.which("zstd") is None:
        raise PreparationError("A new output path and zstd are required for sparse decompression")
    subprocess.run(["zstd", "-d", "--long=28", "--sparse", str(archive), "-o", str(destination)],
                   check=True, stdout=subprocess.DEVNULL)
    if (destination.is_symlink() or not destination.is_file()
            or destination.stat().st_size != expected_size or sha256_file(destination) != digest):
        raise PreparationError("Decompressed rootfs.ext4 size or SHA256 mismatch")


def prepare(candidate: Path, baseline_output: Path, candidate_manifest_sha256: str) -> dict:
    if candidate.is_symlink() or baseline_output.is_symlink():
        raise PreparationError("Upgrade input paths must not be symbolic links")
    candidate = candidate.resolve(strict=True)
    baseline_output = baseline_output.resolve(strict=False)
    if baseline_output.exists() or not baseline_output.parent.is_dir():
        raise PreparationError("Baseline output must be a new directory under an existing parent")
    if baseline_output == candidate or candidate in baseline_output.parents or baseline_output in candidate.parents:
        raise PreparationError("Baseline output must be separate from candidate artifacts")
    candidate_report = verify_candidate(candidate, candidate_manifest_sha256)
    pinned_data = read_regular(BASELINE_FIXTURE, MAX_MANIFEST)
    entries = parse_sums(pinned_data, BASELINE_SUMS_SHA256)
    require_names(entries, BASELINE_ASSETS + ("rootfs.ext4", "guest-manifest.json"))

    staging = Path(tempfile.mkdtemp(prefix=".guest-upgrade-baseline-", dir=baseline_output.parent))
    try:
        # Authenticated size metadata makes the multi-GiB free-space check
        # precise before the image download and decompression begin.
        download_verified(BASELINE_URL + "/guest-manifest.json", staging / "guest-manifest.json",
                          entries["guest-manifest.json"], None, MAX_MANIFEST)
        sizes = guest_sizes(read_regular(staging / "guest-manifest.json", MAX_MANIFEST), entries)
        budget = require_disk_budget(baseline_output.parent, sizes)
        for name in BASELINE_ASSETS:
            expected_size = sizes.get(name)
            maximum = expected_size if expected_size is not None else FALLBACK_SMALL_ASSET_BUDGET
            download_verified(BASELINE_URL + "/" + name, staging / name, entries[name], expected_size, maximum)
        decompress_sparse(staging / "rootfs.ext4.zst", staging / "rootfs.ext4",
                          sizes["rootfs.ext4"], entries["rootfs.ext4"])
        (staging / "SHA256SUMS").write_bytes(pinned_data)
        try:
            baseline_spec = json.loads(read_regular(staging / "build-spec.json", 2 * MIB))
        except (ValueError, UnicodeDecodeError) as exc:
            raise PreparationError("Baseline build-spec.json is invalid JSON") from exc
        report = {
            "schema_version": 1,
            "baseline": {"release": BASELINE_URL, "manifest_sha256": BASELINE_SUMS_SHA256,
                         "runtime_version": runtime_version(baseline_spec),
                         "files": {name: {"sha256": entries[name], "bytes": sizes.get(name, (staging / name).stat().st_size)}
                                   for name in BASELINE_ASSETS + ("rootfs.ext4",)},
                         "free_space_budget_bytes": budget},
            "candidate": candidate_report,
            "candidate_trust": "Manifest digest supplied independently by the calling workflow: the original build job or the source-pinned release fixture. This helper cannot establish that provenance on its own.",
        }
        (staging / "preparation-report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        staging.rename(baseline_output)
        return report
    except BaseException:
        for name in (*BASELINE_ASSETS, "guest-manifest.json", "rootfs.ext4", "SHA256SUMS", "preparation-report.json"):
            (staging / name).unlink(missing_ok=True)
            (staging / (name + ".part")).unlink(missing_ok=True)
        staging.rmdir()
        raise


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("candidate_artifact_dir", type=Path)
    parser.add_argument("baseline_output_dir", type=Path)
    parser.add_argument("--candidate-manifest-sha256", required=True,
                        help="SHA256SUMS digest from the original build job or source-pinned release fixture")
    args = parser.parse_args()
    try:
        report = prepare(args.candidate_artifact_dir, args.baseline_output_dir,
                         args.candidate_manifest_sha256)
    except (PreparationError, OSError, subprocess.CalledProcessError, KeyError, TypeError, ValueError) as exc:
        parser.exit(2, f"Upgrade input preparation failed: {exc}\n")
    print(f"Verified baseline at {args.baseline_output_dir.resolve()}")
    print(f"Candidate manifest SHA256: {report['candidate']['manifest_sha256']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
