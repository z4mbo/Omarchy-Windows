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
    def test_upgrade_and_reboots_require_picker_after_runtime_transaction(self) -> None:
        fixture = SOURCE.with_name('guest-upgrade')
        self.assertNotIn('pacman -Qq zenity', (fixture / 'seed.sh').read_text())
        self.assertIn('pacman -Qq zenity', (fixture / 'upgrade.sh').read_text())
        self.assertIn('pacman -Qq zenity', (fixture / 'reboot.sh').read_text())

    def test_fault_cut_kills_only_owned_qemu_after_complete_nonce_line(self) -> None:
        class Stream:
            def __init__(self):
                self.writes = []

            def write(self, data):
                self.writes.append(data)

            def flush(self):
                pass

            def fileno(self):
                return 7

            def close(self):
                pass

        class Process:
            def __init__(self):
                self.stdin = Stream()
                self.stdout = Stream()
                self.returncode = None
                self.kills = 0
                self.terminates = 0

            def poll(self):
                return self.returncode

            def kill(self):
                self.kills += 1
                self.returncode = -upgrade.signal.SIGKILL

            def wait(self, timeout=None):
                return self.returncode

            def terminate(self):
                self.terminates += 1
                self.returncode = 0
        class Selector:
            def register(self, fileobj, events):
                self.fileobj = fileobj

            def select(self, timeout=None):
                return [(type('Key', (), {'fileobj': self.fileobj})(), None)]

            def close(self):
                pass
        nonce = 'a' * 32
        chunks = iter((b'login:\n', b'Password:\n', b'booting\n',
                       b'PACKAGE_WRITE_CUT_READY:' + nonce.encode() + b'\r\n'))
        proc = Process()
        clock = iter(range(0, 100, 2))
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            preload = root / 'preload'
            preload.mkdir()
            with (mock.patch.object(upgrade, 'qemu_command', return_value=['qemu-system-x86_64']),
                  mock.patch.object(upgrade.subprocess, 'Popen', return_value=proc) as popen,
                  mock.patch.object(upgrade.selectors, 'DefaultSelector', return_value=Selector()),
                  mock.patch.object(upgrade.os, 'read', side_effect=lambda fd, size: next(chunks)),
                  mock.patch.object(upgrade.signal, 'SIGKILL', 9, create=True),
                  mock.patch.object(upgrade.time, 'monotonic', side_effect=lambda: next(clock))):
                upgrade.boot(root, root / 'fault.qcow2', 'qcow2', 'cut', root / 'cut.log',
                             {'PACKAGE_WRITE_NONCE': nonce}, 60,
                             fault_cut_nonce=nonce, preload_dir=preload)
            self.assertEqual(proc.kills, 1)
            self.assertEqual(proc.terminates, 0)
            self.assertTrue(any(b'/mnt/host/cut.sh' in item for item in proc.stdin.writes))
            self.assertIn('mount_tag=preload', ' '.join(popen.call_args.args[0]))

            premature = Process()
            early_chunks = iter((b'PACKAGE_WRITE_CUT_READY:' + nonce.encode() + b'\n',))
            early_clock = iter(range(0, 100, 2))
            with (mock.patch.object(upgrade, 'qemu_command', return_value=['qemu-system-x86_64']),
                  mock.patch.object(upgrade.subprocess, 'Popen', return_value=premature),
                  mock.patch.object(upgrade.selectors, 'DefaultSelector', return_value=Selector()),
                  mock.patch.object(upgrade.os, 'read', side_effect=lambda fd, size: next(early_chunks)),
                  mock.patch.object(upgrade.signal, 'SIGKILL', 9, create=True),
                  mock.patch.object(upgrade.time, 'monotonic', side_effect=lambda: next(early_clock))):
                with self.assertRaisesRegex(RuntimeError, 'before fixture launch'):
                    upgrade.boot(root, root / 'fault.qcow2', 'qcow2', 'cut', root / 'early.log',
                                 {'PACKAGE_WRITE_NONCE': nonce}, 60,
                                 fault_cut_nonce=nonce, preload_dir=preload)
            self.assertEqual(premature.kills, 0)
            self.assertEqual(premature.terminates, 1)

    def test_fault_cut_rejects_embedded_or_wrong_nonce(self) -> None:
        nonce = 'a' * 32
        marker = f'PACKAGE_WRITE_CUT_READY:{nonce}'.encode()
        transcript = bytearray(b'echo PACKAGE_WRITE_CUT_READY:' + nonce.encode() + b'\n')
        found, cursor = upgrade.completed_cut_line(transcript, 0, marker)
        self.assertFalse(found)
        transcript.extend(marker[:-1])
        found, cursor = upgrade.completed_cut_line(transcript, cursor, marker)
        self.assertFalse(found)
        transcript.extend(marker[-1:] + b'\r\n')
        found, cursor = upgrade.completed_cut_line(transcript, cursor, marker)
        self.assertTrue(found)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaisesRegex(ValueError, 'same 32-hex nonce'):
                upgrade.boot(root, root / 'disk', 'qcow2', 'cut', root / 'cut.log',
                             {'PACKAGE_WRITE_NONCE': 'b' * 32}, 5, fault_cut_nonce=nonce)

    def test_candidate_probe_uses_guest_paths_on_any_host(self) -> None:
        checks = upgrade.candidate_checks(35)
        self.assertIn('test -x /usr/local/bin/omarchy-windows-native', checks)
        self.assertNotIn('\\usr\\local\\bin', checks)

    def test_revision38_upgrade_checks_include_lua_move_probe(self) -> None:
        self.assertNotIn('legacy_dispatch_needs_lua', upgrade.candidate_checks(37))
        self.assertIn('legacy_dispatch_needs_lua', upgrade.candidate_checks(38))

    def test_revision40_checks_installed_update_gate_on_older_disks(self) -> None:
        self.assertNotIn('/usr/bin/omarchy-update', upgrade.candidate_checks(39))
        checks = upgrade.candidate_checks(40)
        for path in ('/usr/bin/omarchy-update', '/usr/bin/omarchy-channel-set',
                     '/usr/bin/omarchy-update-available',
                     '/usr/local/lib/try-omarchy/update-gate',
                     '/usr/share/try-omarchy/upstream-commands/omarchy-update'):
            self.assertIn('test -x ' + path, checks)

    def test_revision41_checks_named_routes_and_mapped_proxy_on_older_disks(self) -> None:
        self.assertNotIn('matching_new', upgrade.candidate_checks(40))
        checks = upgrade.candidate_checks(41)
        self.assertIn('omarchy-windows-open explorer', checks)
        self.assertIn('omarchy-windows-open league', checks)
        self.assertIn("grep -Fq 'matching_new'", checks)
        self.assertIn("grep -Fq 'self.mapped = False'", checks)

    def test_revision42_checks_native_layout_conflict_guidance_on_older_disks(self) -> None:
        self.assertNotIn('layout_error_message(exc)', upgrade.candidate_checks(41))
        checks = upgrade.candidate_checks(42)
        self.assertIn('layout_error_message(exc)', checks)
        self.assertIn('Stop showing Windows app', checks)

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
