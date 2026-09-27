from __future__ import annotations

import importlib.util
import os
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("smoke-guest.py")
SPEC = importlib.util.spec_from_file_location("smoke_guest", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
smoke_guest = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(smoke_guest)


class ParseFactsTests(unittest.TestCase):
    def test_ignores_echoed_placeholders_and_keeps_last_real_value(self) -> None:
        transcript = (
            b"printf 'TRYOMARCHY_FACT:yay:%s\\n' \"$(pacman -Q yay)\"\r\n"
            b"\x1b[?2004lTRYOMARCHY_FACT:yay:missing\r\n"
            b"TRYOMARCHY_FACT:sshd:inactive\r\n"
            b"TRYOMARCHY_FACT:yay:present\r\n"
        )
        self.assertEqual(smoke_guest.parse_facts(transcript), {"yay": "present", "sshd": "inactive"})

    def test_tolerates_bad_utf8_and_rejects_quoted_or_escaped_values(self) -> None:
        transcript = (
            b"\xffTRYOMARCHY_FACT:foreign:0\n"
            b"TRYOMARCHY_FACT:quoted:'present'\n"
            b"TRYOMARCHY_FACT:escaped:present\\later\n"
        )
        self.assertEqual(smoke_guest.parse_facts(transcript), {"foreign": "0"})


class NativeGuestFactsTests(unittest.TestCase):
    def test_revision39_fresh_idle_preference_probe(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            service = home / "idle service.qml"
            service.write_text('property string marker: "stay-awake"\n')
            facts, expected = {}, {}
            smoke_guest.add_idle_guest_facts(38, facts, expected, service)
            self.assertNotIn("idle-stay-awake-default", facts)
            smoke_guest.add_idle_guest_facts(39, facts, expected, service)
            self.assertEqual(expected["idle-stay-awake-default"], "yes")
            if os.name != "posix" or not shutil.which("bash"):
                return
            command = facts["idle-stay-awake-default"]
            environment = dict(os.environ, HOME=directory)

            def probe() -> str:
                result = subprocess.run(["bash", "-c", command], env=environment,
                                        capture_output=True, text=True, check=True)
                return result.stdout.strip()

            self.assertEqual(probe(), "no")
            marker = home / ".local/state/omarchy/indicators/stay-awake"
            marker.parent.mkdir(parents=True)
            marker.touch()
            self.assertEqual(probe(), "yes")
            service.write_text("idle handling without marker recognition\n")
            self.assertEqual(probe(), "no")

    def test_fresh_image_requires_runtime_picker_dependency(self) -> None:
        self.assertEqual(smoke_guest.EXPECTED_FACTS["runtime-package"], "4.0.3-5")
        self.assertIn("pacman -Qq zenity", smoke_guest.FACT_CHECKS["windows-app-picker"])

    def test_revision38_lua_probe_rejects_a_broken_guest_helper(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            helper = target / "omarchy_windows_native_layout.py"
            source = MODULE_PATH.resolve().parents[2] / "scripts" / "windows-apps" / helper.name
            shutil.copy2(source, helper)
            facts, expected = {}, {}
            smoke_guest.add_native_guest_facts(38, facts, expected, target)
            self.assertEqual(expected["windows-apps-lua-dispatch"], "yes")
            command = facts["windows-apps-lua-dispatch"]
            code = shlex.split(command.split("python3 -c ", 1)[1].split(" && echo", 1)[0])[0]
            environment = dict(os.environ, PYTHONPATH=str(target), PYTHONDONTWRITEBYTECODE="1")
            self.assertEqual(subprocess.run([sys.executable, "-c", code], env=environment,
                                            capture_output=True, check=False).returncode, 0)
            helper.write_text("class LayoutError(ValueError): pass\n"
                              "def workspace_move_command(*_args): return ['hyprctl', 'dispatch', 'wrong']\n"
                              "def legacy_dispatch_needs_lua(_text): return True\n")
            self.assertNotEqual(subprocess.run([sys.executable, "-c", code], env=environment,
                                               capture_output=True, check=False).returncode, 0)
            old_facts, old_expected = {}, {}
            smoke_guest.add_native_guest_facts(37, old_facts, old_expected, target)
            self.assertNotIn("windows-apps-lua-dispatch", old_facts)

    def test_revision37_fullscreen_probe_detects_wrong_workspace_inheritance(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            helper = target / "omarchy_windows_native_layout.py"
            helper.write_text("def marker_for(ident): return ' [proxy]'\n"
                              "def inherited_workspace(group, known, clients, pid, history):\n"
                              "    return clients[0]['workspace']['id'] if group in known.values() else None\n")
            facts, expected = {}, {}
            smoke_guest.add_native_guest_facts(37, facts, expected, target)
            command = facts["windows-apps-fullscreen-workspace"]
            self.assertEqual(expected["windows-apps-fullscreen-workspace"], "yes")
            code = shlex.split(command.split("python3 -c ", 1)[1].split(" && echo", 1)[0])[0]
            environment = dict(os.environ, PYTHONPATH=str(target), PYTHONDONTWRITEBYTECODE="1")
            self.assertEqual(subprocess.run([sys.executable, "-c", code], env=environment,
                                            capture_output=True, check=False).returncode, 0)
            if os.name == "posix" and shutil.which("bash"):
                self.assertEqual(subprocess.run(["bash", "-c", command], capture_output=True,
                                                text=True, check=True).stdout.strip(), "yes")
            helper.write_text("def marker_for(ident): return ' [proxy]'\n"
                              "def inherited_workspace(group, known, clients, pid, history): return None\n")
            self.assertNotEqual(subprocess.run([sys.executable, "-c", code], env=environment,
                                               capture_output=True, check=False).returncode, 0)
            if os.name == "posix" and shutil.which("bash"):
                self.assertEqual(subprocess.run(["bash", "-c", command], capture_output=True,
                                                text=True, check=True).stdout.strip(), "no")
            older, older_expected = {}, {}
            smoke_guest.add_native_guest_facts(36, older, older_expected, target)
            self.assertNotIn("windows-apps-fullscreen-workspace", older)

    def test_current_native_revision_probe_imports_packaged_native_script(self) -> None:
        revision = smoke_guest.guest_compat_revision()
        self.assertGreaterEqual(revision, 34)
        source = MODULE_PATH.resolve().parents[2] / "scripts" / "windows-apps"
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory)
            for name in ("omarchy-windows-native", "omarchy_windows_native_layout.py",
                         "omarchy_windows_protocol.py"):
                shutil.copy2(source / name, target / name)
            (target / "omarchy-windows-native").chmod(0o755)

            facts, expected = {}, {}
            smoke_guest.add_native_guest_facts(revision, facts, expected, target)
            self.assertEqual(expected["windows-apps-native"], "yes")

            environment = dict(os.environ, PYTHONPATH=str(target), PYTHONDONTWRITEBYTECODE="1")

            def import_probe() -> subprocess.CompletedProcess[bytes]:
                command = ([sys.executable, str(target / "omarchy-windows-native"), "--help"]
                           if os.name == "posix" else
                           [sys.executable, "-c", "import omarchy_windows_native_layout, omarchy_windows_protocol"])
                return subprocess.run(command,
                                      env=environment, capture_output=True, check=False)

            result = import_probe()
            self.assertEqual(result.returncode, 0, result.stderr.decode(errors="replace"))

            def probe() -> str:
                result = subprocess.run(["bash", "-c", facts["windows-apps-native"]],
                                        text=True, capture_output=True, check=True)
                return result.stdout.strip()

            if os.name == "posix":
                self.assertEqual(probe(), "yes")
            (target / "omarchy_windows_native_layout.py").unlink()
            self.assertNotEqual(import_probe().returncode, 0)
            if os.name == "posix":
                self.assertEqual(probe(), "no")
            shutil.copy2(source / "omarchy_windows_native_layout.py",
                         target / "omarchy_windows_native_layout.py")
            (target / "omarchy_windows_protocol.py").unlink()
            self.assertNotEqual(import_probe().returncode, 0)
            if os.name == "posix":
                self.assertEqual(probe(), "no")

    def test_older_revision_does_not_require_native_mode(self) -> None:
        facts, expected = {}, {}
        smoke_guest.add_native_guest_facts(33, facts, expected)
        self.assertNotIn("windows-apps-native", facts)
        self.assertNotIn("windows-apps-native", expected)


if __name__ == "__main__":
    unittest.main()
