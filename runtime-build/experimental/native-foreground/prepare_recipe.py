#!/usr/bin/env python3
"""Create a separately pinned QEMU recipe for the foreground experiment."""

from __future__ import annotations

import hashlib
import json
import shutil
import sys
from pathlib import Path


EXPERIMENTAL_NAME = "winq-emu-alpha10-native-foreground-experimental"
PATCH = "0013-bound-native-foreground-permission-to-sdl-grant.patch"
PATCH_SHA256 = "3f5cc0f5d8f2e68574fc28775d87efa443065b15203b26cb6c2899265068bd59"


def replace_once(path: Path, old: str, new: str) -> None:
    value = path.read_text(encoding="utf-8")
    if value.count(old) != 1:
        raise RuntimeError(f"expected exactly one {old!r} in {path}")
    path.write_text(value.replace(old, new), encoding="utf-8", newline="\n")


def prepare(destination: Path) -> None:
    source = Path(__file__).resolve().parents[2]
    if destination.exists():
        raise ValueError(f"destination already exists: {destination}")
    patch = source / "patches" / "qemu" / PATCH
    if hashlib.sha256(patch.read_bytes()).hexdigest() != PATCH_SHA256:
        raise ValueError("experimental native foreground patch hash changed")
    shutil.copytree(source, destination, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))

    lock_path = destination / "sources.lock.json"
    lock = json.loads(lock_path.read_text(encoding="utf-8"))
    lock["name"] = EXPERIMENTAL_NAME
    lock["qemu"]["patches"].append(
        {"file": f"patches/qemu/{PATCH}", "sha256": PATCH_SHA256}
    )
    lock_path.write_text(json.dumps(lock, indent=2) + "\n", encoding="utf-8", newline="\n")

    for name in ("build.sh", "verify.py"):
        path = destination / name
        for kind in ("portable", "source"):
            replace_once(path, f"winq-emu-alpha10-{kind}.zip", f"{EXPERIMENTAL_NAME}-{kind}.zip")
    replace_once(
        destination / "build.sh",
        "WINQ-EMU runtime for Try Omarchy",
        "EXPERIMENTAL native foreground runtime for Omarchy",
    )
    print(f"prepared {EXPERIMENTAL_NAME} recipe at {destination}")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} DESTINATION")
    prepare(Path(sys.argv[1]))
