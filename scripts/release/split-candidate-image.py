#!/usr/bin/env python3
"""Split a verified guest rootfs archive into connector-sized CI test artifacts."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path


PART_BYTES = 384 * 1024 * 1024
MAX_PARTS = 8
ARCHIVE_NAME = "rootfs.ext4.zst"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def expected_archive_sha(manifest: Path) -> str:
    found: list[str] = []
    for line in manifest.read_text(encoding="ascii").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (\S+)", line)
        if match is None:
            raise ValueError("invalid candidate SHA256SUMS entry")
        if match.group(2) == ARCHIVE_NAME:
            found.append(match.group(1))
    if len(found) != 1:
        raise ValueError("candidate manifest must contain exactly one rootfs archive")
    return found[0]


def split_candidate(archive: Path, manifest: Path, output: Path,
                    part_bytes: int = PART_BYTES, max_parts: int = MAX_PARTS) -> dict:
    if part_bytes <= 0 or max_parts <= 0:
        raise ValueError("part size and count must be positive")
    if archive.name != ARCHIVE_NAME or not archive.is_file() or archive.is_symlink():
        raise ValueError("expected a regular rootfs.ext4.zst archive")
    if not manifest.is_file() or manifest.is_symlink():
        raise ValueError("expected a regular SHA256SUMS manifest")
    size = archive.stat().st_size
    if size <= 0 or size > part_bytes * max_parts:
        raise ValueError("rootfs archive exceeds bounded download part budget")
    expected = expected_archive_sha(manifest)
    if sha256(archive) != expected:
        raise ValueError("rootfs archive differs from candidate SHA256SUMS")
    output.mkdir(parents=True, exist_ok=False)
    parts = []
    with archive.open("rb") as source:
        for index in range(max_parts):
            data = source.read(part_bytes)
            if not data:
                break
            name = f"{ARCHIVE_NAME}.part-{index:02d}"
            (output / name).write_bytes(data)
            parts.append({"name": name, "bytes": len(data),
                          "sha256": hashlib.sha256(data).hexdigest()})
        if source.read(1):
            raise ValueError("rootfs archive has more parts than allowed")
    if sum(part["bytes"] for part in parts) != size:
        raise ValueError("rootfs part size mismatch")
    reassembled = hashlib.sha256()
    for part in parts:
        path = output / part["name"]
        if path.stat().st_size != part["bytes"] or sha256(path) != part["sha256"]:
            raise ValueError("rootfs part changed before upload")
        with path.open("rb") as source:
            for block in iter(lambda: source.read(1024 * 1024), b""):
                reassembled.update(block)
    if reassembled.hexdigest() != expected:
        raise ValueError("ordered rootfs parts do not reproduce the original archive")
    index = {"schemaVersion": 1, "original": {"name": ARCHIVE_NAME,
             "bytes": size, "sha256": expected}, "sha256sumsSha256": sha256(manifest),
             "partBytesLimit": part_bytes, "parts": parts}
    (output / "parts-manifest.json").write_text(
        json.dumps(index, sort_keys=True, indent=2) + "\n", encoding="utf-8")
    return index


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("archive", type=Path)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("output", type=Path)
    args = parser.parse_args()
    index = split_candidate(args.archive, args.manifest, args.output)
    print(f"{ARCHIVE_NAME}: {index['original']['bytes']} bytes in {len(index['parts'])} verified parts")


if __name__ == "__main__":
    main()
