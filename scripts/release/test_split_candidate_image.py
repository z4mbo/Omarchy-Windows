"""Offline checks for bounded, authenticated candidate image parts."""

from __future__ import annotations

import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("split-candidate-image.py")
spec = importlib.util.spec_from_file_location("split_candidate_image", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class SplitCandidateImageTests(unittest.TestCase):
    def test_parts_reassemble_exact_manifest_authenticated_archive(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "rootfs.ext4.zst"
            payload = bytes(range(101))
            archive.write_bytes(payload)
            manifest = root / "SHA256SUMS"
            original_sha = hashlib.sha256(payload).hexdigest()
            manifest.write_text(f"{original_sha}  rootfs.ext4.zst\n", encoding="ascii")
            index = module.split_candidate(archive, manifest, root / "parts", part_bytes=17, max_parts=8)
            self.assertEqual(index["original"], {"name": archive.name, "bytes": len(payload),
                                                  "sha256": original_sha})
            self.assertEqual(len(index["parts"]), 6)
            self.assertEqual(b"".join((root / "parts" / item["name"]).read_bytes()
                                      for item in index["parts"]), payload)
            self.assertEqual(json.loads((root / "parts" / "parts-manifest.json").read_text()), index)

    def test_rejects_bad_manifest_before_creating_parts(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "rootfs.ext4.zst"
            archive.write_bytes(b"candidate")
            manifest = root / "SHA256SUMS"
            manifest.write_text(f"{'0' * 64}  rootfs.ext4.zst\n", encoding="ascii")
            with self.assertRaisesRegex(ValueError, "differs"):
                module.split_candidate(archive, manifest, root / "parts", part_bytes=3)
            self.assertFalse((root / "parts").exists())

    def test_rejects_archive_over_part_budget(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive = root / "rootfs.ext4.zst"
            archive.write_bytes(b"123456789")
            manifest = root / "SHA256SUMS"
            manifest.write_text(f"{hashlib.sha256(archive.read_bytes()).hexdigest()}  rootfs.ext4.zst\n",
                                encoding="ascii")
            with self.assertRaisesRegex(ValueError, "budget"):
                module.split_candidate(archive, manifest, root / "parts", part_bytes=4, max_parts=2)


if __name__ == "__main__":
    unittest.main()
