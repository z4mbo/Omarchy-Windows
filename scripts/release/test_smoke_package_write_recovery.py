"""Offline safety and orchestration tests for the real package-write CI gate."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock


SOURCE = Path(__file__).with_name('smoke-package-write-recovery.py')
SPEC = importlib.util.spec_from_file_location('package_write_recovery', SOURCE)
assert SPEC is not None and SPEC.loader is not None
recovery = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(recovery)


class RecoveryHarnessTests(unittest.TestCase):
    def test_source_chain_rejects_parent_traversal_and_wrong_format(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'persistent.qcow2'
            source.write_bytes(b'completed upgrade image')
            self.assertRaises(ValueError, recovery.canonical_path, root / 'sub' / '..', existing=False)
            with mock.patch.object(recovery, 'image_info', return_value=[{
                    'filename': str(source), 'format': 'raw', 'virtual-size': 24 * 1024**3}]):
                with self.assertRaisesRegex(RuntimeError, 'format differs'):
                    recovery.source_chain(source, 'qcow2')

    def test_disk_guard_runs_before_creating_test_images(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate = root / 'candidate'
            candidate.mkdir()
            for asset in ('vmlinuz-linux', 'initramfs-linux.img', 'build-spec.json'):
                (candidate / asset).write_bytes(b'candidate')
            source = root / 'persistent.qcow2'
            source.write_bytes(b'completed upgrade')
            work = root / 'recovery'
            chain = [{'path': str(source), 'format': 'qcow2'}]
            with (mock.patch.object(recovery.os, 'access', return_value=True),
                  mock.patch.object(recovery, 'source_chain', return_value=chain),
                  mock.patch.object(recovery.shutil, 'disk_usage',
                                    return_value=SimpleNamespace(free=recovery.MIN_FREE_BYTES - 1)),
                  mock.patch.object(recovery.subprocess, 'run') as run,
                  mock.patch.object(sys, 'argv', [str(SOURCE), str(candidate), str(source), str(work)])):
                with self.assertRaises(SystemExit):
                    recovery.main()
            self.assertFalse(work.exists())
            run.assert_not_called()

    def run_disposable_flow(self, *, fail_at=None, disk_after_fail=False, interrupted=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate = root / 'candidate'
            candidate.mkdir()
            for asset in ('vmlinuz-linux', 'initramfs-linux.img', 'build-spec.json'):
                (candidate / asset).write_bytes(b'candidate')
            source = root / 'persistent.qcow2'
            source.write_bytes(b'completed prior five-boot upgrade')
            source_digest = recovery.sha256(source)
            work = root / 'recovery'
            chain = [{'path': str(source), 'format': 'qcow2', 'sha256': source_digest}]
            calls = []

            def create(backing, backing_format, child):
                calls.append(('create', backing.name, backing_format, child.name))
                child.write_bytes(b'disposable child of ' + backing.name.encode())

            def backup(seed, destination):
                calls.append(('backup', seed.name, destination.name))
                destination.write_bytes(b'independent stopped image')

            def boot(artifacts, disk, disk_format, phase, log, env, timeout, **options):
                calls.append(('boot', disk.name, phase, disk_format, options))
                log.write_text(phase)
                if phase == fail_at:
                    if interrupted:
                        raise KeyboardInterrupt()
                    raise RuntimeError('injected cut failure')

            free = SimpleNamespace(free=20 * 1024**3)
            usage = ([free, OSError('injected disk metric failure')]
                     if disk_after_fail else [free, free])
            args = [str(SOURCE), str(candidate), str(source), str(work), '--source-format', 'qcow2']
            with (mock.patch.object(recovery.os, 'access', return_value=True),
                  mock.patch.object(recovery.shutil, 'disk_usage', side_effect=usage),
                  mock.patch.object(recovery, 'source_chain', return_value=chain) as chain_call,
                  mock.patch.object(recovery, 'create_child', side_effect=create),
                  mock.patch.object(recovery, 'flattened_backup', side_effect=backup),
                  mock.patch.object(recovery, 'load_boot_harness',
                                    return_value=SimpleNamespace(boot=boot)),
                  mock.patch.object(recovery, 'image_sizes', return_value={}),
                  mock.patch.object(recovery.subprocess, 'run') as compile_call,
                  mock.patch.object(sys, 'argv', args)):
                if disk_after_fail:
                    with self.assertRaisesRegex(OSError, 'injected disk metric failure'):
                        recovery.main()
                elif interrupted:
                    with self.assertRaises(KeyboardInterrupt):
                        recovery.main()
                elif fail_at is None:
                    recovery.main()
                else:
                    with self.assertRaisesRegex(RuntimeError, 'injected cut failure'):
                        recovery.main()
            self.assertEqual(recovery.sha256(source), source_digest)
            self.assertEqual(chain_call.call_count, 2)
            self.assertEqual(compile_call.call_count, 1)
            self.assertEqual(calls[:3], [
                ('create', 'persistent.qcow2', 'qcow2', 'seed.qcow2'),
                ('boot', 'seed.qcow2', 'seed', 'qcow2', {}),
                ('backup', 'seed.qcow2', 'pre-cut-backup.qcow2'),
            ])
            report = json.loads((work / 'result.json').read_text())
            if fail_at is None or disk_after_fail:
                self.assertEqual([entry[2] for entry in calls if entry[0] == 'boot'],
                                 ['seed', 'cut', 'torn', 'restore', 'reboot'])
                self.assertEqual([entry[1:4] for entry in calls if entry[0] == 'create'], [
                    ('persistent.qcow2', 'qcow2', 'seed.qcow2'),
                    ('pre-cut-backup.qcow2', 'qcow2', 'fault.qcow2'),
                    ('pre-cut-backup.qcow2', 'qcow2', 'restored.qcow2'),
                ])
                cut = next(entry for entry in calls if entry[0] == 'boot' and entry[2] == 'cut')
                self.assertEqual(cut[4]['fault_cut_nonce'], report['fault_nonce'])
                self.assertEqual(cut[4]['preload_dir'], work / 'preload')
                self.assertEqual(report['passed'], not disk_after_fail)
                self.assertEqual(report['phases_completed'], ['seed', 'cut', 'torn', 'restore', 'reboot'])
                if disk_after_fail:
                    self.assertIn('injected disk metric failure', report['free_after_error'])
            else:
                self.assertFalse(report['passed'])
                self.assertEqual(report['phases_completed'], ['seed'])
                if not interrupted:
                    self.assertIn('injected cut failure', report['error'])

    def test_uses_independent_backup_then_restored_child(self) -> None:
        self.run_disposable_flow()

    def test_failure_still_rechecks_immutable_source_and_reports_failure(self) -> None:
        self.run_disposable_flow(fail_at='cut')

    def test_metric_failure_still_rechecks_source_and_marks_report_failed(self) -> None:
        self.run_disposable_flow(disk_after_fail=True)

    def test_interrupt_cannot_mark_incomplete_sequence_passed(self) -> None:
        self.run_disposable_flow(fail_at='cut', interrupted=True)


if __name__ == '__main__':
    unittest.main()
