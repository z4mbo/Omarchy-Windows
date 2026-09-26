import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class StablePinTests(unittest.TestCase):
    def test_stable_release_pin_and_mismatched_version(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            version = re.search(r'currentVersion\s*=\s*"([^"]+)"', (ROOT / "app/update.go").read_text()).group(1)
            for relative in ["app/manifest.go", "app/update.go", "app/cmd/sign-update/main.go"]:
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text((ROOT / relative).read_text().replace(version, "v1.0.0"))
            fixture = root / "app/testdata/SHA256SUMS.v1.0.0"
            fixture.parent.mkdir(parents=True)
            fixture.write_bytes((ROOT / f"app/testdata/SHA256SUMS.{version}").read_bytes())
            command = [sys.executable, str(ROOT / "scripts/release/validate-pin.py")]
            result = subprocess.run(command + ["v1.0.0", "--root", str(root), "--repository", "omacom/try-omarchy-windows"], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            result = subprocess.run(command + ["v1.0.1", "--root", str(root), "--repository", "omacom/try-omarchy-windows"], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("currentVersion", result.stderr)

    def test_fork_release_rejects_inherited_key_and_accepts_only_exact_bootstrap(self):
        command = [sys.executable, str(ROOT / "scripts/release/validate-pin.py")]
        bootstrap = command + ["v0.0.20-preview", "--allow-upstream-bootstrap", "--require-independent-key"]
        result = subprocess.run(bootstrap, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        result = subprocess.run(command + ["v0.0.20-preview", "--require-independent-key"], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("own Ed25519", result.stderr)

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            inherited = "f1edc8c2fc8fc8a7a108832eb93a9d9f2f8c07c5547fc4e4cb805c3b1615c9cd"
            test_key = "ab" * 32  # A fixture, never a production update key.
            for relative in ["app/manifest.go", "app/update.go", "app/cmd/sign-update/main.go"]:
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                source = (ROOT / relative).read_text().replace(inherited, test_key)
                if relative == "app/manifest.go":
                    source = source.replace(
                        "https://github.com/omacom/try-omarchy-windows/releases/download/v0.0.20-preview",
                        "https://github.com/z4mbo/Omarchy-Windows/releases/download/v0.0.20-preview",
                    )
                path.write_text(source)
            fixture = root / "app/testdata/SHA256SUMS.v0.0.20-preview"
            fixture.parent.mkdir(parents=True)
            fixture.write_bytes((ROOT / "app/testdata/SHA256SUMS.v0.0.20-preview").read_bytes())
            result = subprocess.run(
                command + ["v0.0.20-preview", "--root", str(root), "--require-independent-key"],
                capture_output=True, text=True,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
