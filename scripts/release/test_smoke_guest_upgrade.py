"""Offline regression checks for the five-boot upgrade runner."""

from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SOURCE = Path(__file__).with_name('smoke-guest-upgrade.py')
SPEC = importlib.util.spec_from_file_location('smoke_guest_upgrade', SOURCE)
assert SPEC is not None and SPEC.loader is not None
upgrade = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(upgrade)


def write_artifacts(directory: Path, *, baseline: bool) -> None:
    directory.mkdir()
    (directory / 'build-spec.json').write_text(json.dumps({
        'runtime': {'kernelCommandLine': 'root=/dev/vda console=tty0 console=hvc0'},
        'upstream': {'version': '4.0.3', 'packageRelease': 4},
    }))
    (directory / 'vmlinuz-linux').write_bytes(b'kernel')
    (directory / 'initramfs-linux.img').write_bytes(b'initramfs')
    if baseline:
        (directory / 'rootfs.ext4').write_bytes(b'unchanged factory')


class UpgradeHarnessTests(unittest.TestCase):
    def test_candidate_probe_uses_guest_paths_on_any_host(self) -> None:
        checks = upgrade.candidate_checks(35)
        self.assertIn('test -x /usr/local/bin/omarchy-windows-native', checks)
        self.assertNotIn('\\usr\\local\\bin', checks)

    def test_qcow2_overlay_uses_raw_backing_without_copying_or_mutating_it(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            baseline = root / 'factory.ext4'
            baseline.write_bytes(b'factory')
            work = root / 'work'
            work.mkdir()
            with mock.patch.object(upgrade.subprocess, 'run') as run:
                disk, disk_format = upgrade.create_disk(baseline, work, 'qcow2')
            self.assertEqual((disk, disk_format), (work / 'persistent.qcow2', 'qcow2'))
            self.assertEqual(baseline.read_bytes(), b'factory')
            run.assert_called_once_with([
                'qemu-img', 'create', '-f', 'qcow2', '-F', 'raw', '-b', str(baseline),
                str(disk), str(24 * 1024**3),
            ], check=True)

    def test_all_boots_use_selected_disk_format_and_correct_external_image(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            baseline, candidate, work = root / 'baseline', root / 'candidate', root / 'work'
            write_artifacts(baseline, baseline=True)
            write_artifacts(candidate, baseline=False)
            overlay = work / 'persistent.qcow2'
            calls = []

            def record_boot(artifacts, disk, disk_format, phase, log, environment, timeout, checks):
                command = upgrade.qemu_command(artifacts, disk, disk_format)
                calls.append((artifacts, phase, log.name, checks, command[command.index('-drive') + 1]))

            with (mock.patch.object(upgrade.os, 'access', return_value=True),
                  mock.patch.object(upgrade, 'create_disk', return_value=(overlay, 'qcow2')),
                  mock.patch.object(upgrade, 'boot', side_effect=record_boot),
                  mock.patch.object(sys, 'argv', [str(SOURCE), str(baseline), str(candidate), str(work),
                                                  '--candidate-compat-revision', '35'])):
                upgrade.main()

            self.assertEqual([item[1] for item in calls], ['seed', 'upgrade', 'reboot', 'reboot', 'reboot'])
            self.assertEqual([item[2] for item in calls], ['01-seed.log', '02-upgrade.log',
                                                           '03-reboot.log', '04-backward-image.log',
                                                           '05-return-to-candidate.log'])
            self.assertEqual([item[0] for item in calls], [baseline, candidate, candidate, baseline, candidate])
            self.assertTrue(all(item[4] == f'file={overlay},format=qcow2,if=virtio' for item in calls))
            self.assertEqual([bool(item[3]) for item in calls], [False, True, True, False, True])
            self.assertEqual((baseline / 'rootfs.ext4').read_bytes(), b'unchanged factory')

    @unittest.skipUnless(os.name == 'posix' and shutil.which('bash'), 'guest shell requires Linux')
    def test_candidate_probe_checks_revision_native_import_and_occlusion_behavior(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / 'omarchy-windows-native'
            binary.write_text('#!/usr/bin/env python3\nimport omarchy_windows_native_layout\n')
            binary.chmod(0o755)
            layout = root / 'omarchy_windows_native_layout.py'
            layout.write_text('def occlusion_limit(document):\n'
                              '    return document["capabilities"]["occlusions"]["maxRectsPerWindow"]\n')
            compat = root / 'compat-version'
            kernel = subprocess.check_output(['uname', '-r'], text=True).strip()
            compat.write_text(f'35:{kernel}\n')
            checks = upgrade.candidate_checks(35, str(compat), str(root))
            self.assertEqual(subprocess.run(['bash', '-c', checks], check=False).returncode, 0)
            compat.write_text(f'34:{kernel}\n')
            self.assertNotEqual(subprocess.run(['bash', '-c', checks], check=False).returncode, 0)
            compat.write_text(f'35:{kernel}\n')
            layout.write_text('def occlusion_limit(document):\n    return 0\n')
            self.assertNotEqual(subprocess.run(['bash', '-c', checks], check=False).returncode, 0)

    @unittest.skipUnless(os.name == 'posix' and shutil.which('bash'), 'guest shell requires Linux')
    def test_revision37_upgrade_probe_requires_fullscreen_workspace_behavior(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            native = root / 'omarchy-windows-native'
            native.write_text('#!/usr/bin/env python3\nimport omarchy_windows_native_layout\n')
            native.chmod(0o755)
            layout = root / 'omarchy_windows_native_layout.py'
            layout.write_text('def occlusion_limit(document): return 16\n'
                              'def marker_for(ident): return " [proxy]"\n'
                              'def inherited_workspace(group, known, clients, pid, history):\n'
                              '    return clients[0]["workspace"]["id"] if group in known.values() else None\n')
            compat = root / 'compat-version'
            kernel = subprocess.check_output(['uname', '-r'], text=True).strip()
            compat.write_text(f'37:{kernel}\n')
            checks = upgrade.candidate_checks(37, str(compat), str(root))
            self.assertEqual(subprocess.run(['bash', '-c', checks], check=False).returncode, 0)
            layout.write_text('def occlusion_limit(document): return 16\n'
                              'def marker_for(ident): return " [proxy]"\n'
                              'def inherited_workspace(group, known, clients, pid, history): return None\n')
            self.assertNotEqual(subprocess.run(['bash', '-c', checks], check=False).returncode, 0)


if __name__ == '__main__':
    unittest.main()
