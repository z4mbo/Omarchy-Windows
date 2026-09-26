"""Small offline fixtures for authenticated upgrade input preparation."""

from __future__ import annotations

import hashlib
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("prepare-guest-upgrade.py")
spec = importlib.util.spec_from_file_location("prepare_guest_upgrade", SCRIPT)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def sha(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def sums(assets: dict[str, bytes]) -> bytes:
    return "".join(f"{sha(data)}  {name}\n" for name, data in assets.items()).encode()


def fixture(root: Path) -> tuple[Path, Path, dict[str, bytes], bytes]:
    spec_data = json.dumps({"upstream": {"version": "4.0.3", "packageRelease": 2},
                            "runtime": {"kernelCommandLine": "console=ttyS0"}}).encode()
    candidate = root / "candidate"
    candidate.mkdir()
    candidate_assets = {"build-spec.json": spec_data, "vmlinuz-linux": b"candidate kernel",
                        "initramfs-linux.img": b"candidate initramfs",
                        "rootfs.ext4": b"candidate rootfs not retained",
                        "rootfs.ext4.zst": b"candidate archive not retained"}
    candidate_sums = sums(candidate_assets)
    (candidate / "SHA256SUMS").write_bytes(candidate_sums)
    for name in module.CANDIDATE_ASSETS:
        (candidate / name).write_bytes(candidate_assets[name])

    baseline_assets = {"build-spec.json": spec_data, "vmlinuz-linux": b"older kernel",
                       "initramfs-linux.img": b"older initramfs",
                       "rootfs.ext4.zst": b"compressed fixture", "rootfs.ext4": b"raw fixture"}
    metadata = {"schemaVersion": 1, "artifacts": [
        {"path": name, "bytes": len(data), "sha256": sha(data)}
        for name, data in baseline_assets.items()]}
    baseline_assets["guest-manifest.json"] = json.dumps(metadata).encode()
    pinned_sums = sums(baseline_assets)
    fixture_path = root / "pinned-SHA256SUMS"
    fixture_path.write_bytes(pinned_sums)
    return candidate, fixture_path, baseline_assets, candidate_sums


class FakeResponse(io.BytesIO):
    def __init__(self, data: bytes, url: str):
        super().__init__(data)
        self.url = url

    def geturl(self) -> str:
        return self.url


class PrepareUpgradeTests(unittest.TestCase):
    def test_repository_baseline_fixture_matches_durable_pin(self):
        data = module.BASELINE_FIXTURE.read_bytes()
        self.assertEqual(sha(data), module.BASELINE_SUMS_SHA256)
        entries = module.parse_sums(data, module.BASELINE_SUMS_SHA256)
        module.require_names(entries, module.BASELINE_ASSETS + ("rootfs.ext4", "guest-manifest.json"))

    def test_prepares_baseline_and_checks_same_run_candidate_subset(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate, pinned_fixture, assets, candidate_sums = fixture(root)
            baseline_output = root / "baseline"

            def fake_download(url, destination, digest, expected_size, maximum):
                data = assets[url.rsplit("/", 1)[-1]]
                self.assertEqual(sha(data), digest)
                self.assertLessEqual(len(data), maximum)
                if expected_size is not None:
                    self.assertEqual(len(data), expected_size)
                destination.write_bytes(data)

            def fake_decompress(archive, destination, size, digest):
                self.assertEqual(archive.read_bytes(), assets["rootfs.ext4.zst"])
                data = assets["rootfs.ext4"]
                self.assertEqual((len(data), sha(data)), (size, digest))
                destination.write_bytes(data)

            with (mock.patch.object(module, "BASELINE_FIXTURE", pinned_fixture),
                  mock.patch.object(module, "BASELINE_SUMS_SHA256", sha(pinned_fixture.read_bytes())),
                  mock.patch.object(module, "download_verified", side_effect=fake_download) as download,
                  mock.patch.object(module, "decompress_sparse", side_effect=fake_decompress),
                  mock.patch.object(module.shutil, "disk_usage", return_value=SimpleNamespace(free=5 * module.GIB))):
                report = module.prepare(candidate, baseline_output, sha(candidate_sums))
            self.assertEqual(download.call_count, 5)  # Four boot assets plus authenticated size metadata.
            self.assertEqual((baseline_output / "rootfs.ext4").read_bytes(), assets["rootfs.ext4"])
            self.assertEqual(report["candidate"]["manifest_sha256"], sha(candidate_sums))
            self.assertEqual(report["candidate"]["runtime_version"], "4.0.3-2")
            self.assertEqual(report["candidate"]["skipped_manifest_entries"],
                             ["rootfs.ext4", "rootfs.ext4.zst"])
            self.assertEqual(json.loads((baseline_output / "preparation-report.json").read_text()), report)

    def test_rejects_unpinned_or_corrupt_candidate_before_download(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate, _pin, _assets, manifest = fixture(root)
            with self.assertRaisesRegex(module.PreparationError, "independent digest"):
                module.prepare(candidate, root / "baseline", "0" * 64)
            (candidate / "vmlinuz-linux").write_bytes(b"wrong")
            with self.assertRaisesRegex(module.PreparationError, "SHA256 mismatch"):
                module.prepare(candidate, root / "baseline", sha(manifest))
            self.assertFalse((root / "baseline").exists())

    def test_low_disk_budget_stops_before_large_download_and_cleans_staging(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate, pinned_fixture, assets, manifest = fixture(root)

            def metadata_only(url, destination, _digest, _expected_size, _maximum):
                self.assertTrue(url.endswith("guest-manifest.json"))
                destination.write_bytes(assets["guest-manifest.json"])

            with (mock.patch.object(module, "BASELINE_FIXTURE", pinned_fixture),
                  mock.patch.object(module, "BASELINE_SUMS_SHA256", sha(pinned_fixture.read_bytes())),
                  mock.patch.object(module, "download_verified", side_effect=metadata_only) as download,
                  mock.patch.object(module.shutil, "disk_usage", return_value=SimpleNamespace(free=1))):
                with self.assertRaisesRegex(module.PreparationError, "free bytes"):
                    module.prepare(candidate, root / "baseline", sha(manifest))
            self.assertEqual(download.call_count, 1)
            self.assertEqual(sorted(path.name for path in root.iterdir()), ["candidate", "pinned-SHA256SUMS"])

    def test_download_enforces_exact_size_hash_and_https(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "asset"
            url = module.BASELINE_URL + "/build-spec.json"
            for payload, expected, final_url, error in (
                (b"abc", 2, url, "exceeds"),
                (b"abc", 4, url, "size or SHA256 mismatch"),
                (b"abc", 3, url, "size or SHA256 mismatch"),
                (b"abc", 3, "http://example.test/asset", "outside HTTPS"),
            ):
                with self.subTest(error=error):
                    with mock.patch.object(module.urllib.request, "urlopen",
                                           return_value=FakeResponse(payload, final_url)):
                        with self.assertRaisesRegex(module.PreparationError, error):
                            module.download_verified(url, target, sha(b"different"), expected, expected)
                    self.assertFalse(target.exists())
                    self.assertFalse(target.with_name("asset.part").exists())
            with mock.patch.object(module.urllib.request, "urlopen", return_value=FakeResponse(b"abc", url)):
                module.download_verified(url, target, sha(b"abc"), 3, 3)
            self.assertEqual(target.read_bytes(), b"abc")

    def test_decompressed_rootfs_digest_is_checked(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            archive, output = root / "a.zst", root / "rootfs.ext4"
            archive.write_bytes(b"verified compressed fixture")

            def fake_run(_args, **_kwargs):
                output.write_bytes(b"wrong")

            with (mock.patch.object(module.shutil, "which", return_value="zstd"),
                  mock.patch.object(module.subprocess, "run", side_effect=fake_run)):
                with self.assertRaisesRegex(module.PreparationError, "SHA256 mismatch"):
                    module.decompress_sparse(archive, output, 5, sha(b"right"))


if __name__ == "__main__":
    unittest.main()
