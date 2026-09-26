#!/usr/bin/env python3
"""Make an isolated, pinned QEMU recipe for the Windows balloon experiment."""

from __future__ import annotations

import hashlib
import json
import shutil
import sys
from pathlib import Path


EXPERIMENTAL_NAME = "winq-emu-alpha10-balloon-experimental"
PATCH_NAME = "0013-windows-recommit-discarded-anonymous-ram.patch"


def replace_once(path: Path, old: str, new: str) -> None:
    value = path.read_text(encoding="utf-8")
    if value.count(old) != 1:
        raise RuntimeError(f"expected exactly one {old!r} in {path}")
    path.write_text(value.replace(old, new), encoding="utf-8", newline="\n")


def prepare(destination: Path) -> None:
    source = Path(__file__).resolve().parents[2]
    patch = Path(__file__).with_name("0001-windows-recommit-discarded-anonymous-ram.patch")
    if destination.exists():
        raise ValueError(f"destination already exists: {destination}")
    shutil.copytree(source, destination, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))

    copied_patch = destination / "patches" / "qemu" / PATCH_NAME
    shutil.copyfile(patch, copied_patch)
    lock_path = destination / "sources.lock.json"
    lock = json.loads(lock_path.read_text(encoding="utf-8"))
    lock["name"] = EXPERIMENTAL_NAME
    lock["qemu"]["patches"].append(
        {
            "file": f"patches/qemu/{PATCH_NAME}",
            "sha256": hashlib.sha256(copied_patch.read_bytes()).hexdigest(),
        }
    )
    lock_path.write_text(json.dumps(lock, indent=2) + "\n", encoding="utf-8", newline="\n")

    for name in ("build.sh", "verify.py"):
        path = destination / name
        for kind in ("portable", "source"):
            replace_once(
                path,
                f"winq-emu-alpha10-{kind}.zip",
                f"{EXPERIMENTAL_NAME}-{kind}.zip",
            )

    replace_once(
        destination / "build.sh",
        "WINQ-EMU runtime for Try Omarchy",
        "EXPERIMENTAL Windows balloon runtime for Omarchy",
    )
    print(f"prepared {EXPERIMENTAL_NAME} recipe at {destination}")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} DESTINATION")
    prepare(Path(sys.argv[1]))
