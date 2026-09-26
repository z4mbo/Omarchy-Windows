#!/usr/bin/env python3
"""Crash a disposable package write, then prove an offline backup restores it."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
from pathlib import Path
import secrets
import shutil
import stat
import subprocess


FIXTURES = Path(__file__).resolve().parent / 'package-write-recovery'
MIN_FREE_BYTES = 10 * 1024**3


def canonical_path(path: Path, *, existing: bool) -> Path:
    """Resolve aliases only after rejecting links and dot-dot traversal."""
    path = path.absolute()
    if '..' in path.parts or any(parent.is_symlink() for parent in (path, *path.parents)):
        raise ValueError(f'path has symlink or parent traversal: {path}')
    if existing:
        return path.resolve(strict=True)
    return path.parent.resolve(strict=True) / path.name


def sha256(path: Path) -> str:
    import hashlib

    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def image_info(path: Path, *, backing_chain: bool = False):
    command = ['qemu-img', 'info', '--output=json']
    if backing_chain:
        command.append('--backing-chain')
    command.append(str(path))
    return json.loads(subprocess.check_output(command, text=True))


def source_chain(source: Path, expected_format: str) -> list[dict]:
    """Pin every local file in the existing upgrade image's backing chain."""
    images = image_info(source, backing_chain=True)
    if not isinstance(images, list) or not 1 <= len(images) <= 4:
        raise RuntimeError('upgrade input has an invalid backing chain')
    result = []
    previous = None
    for index, image in enumerate(images):
        if not isinstance(image, dict) or image.get('format') not in ('qcow2', 'raw'):
            raise RuntimeError('upgrade input has an unsupported image format')
        if index == 0 and image['format'] != expected_format:
            raise RuntimeError('upgrade input format differs from --source-format')
        filename = image.get('filename')
        if not isinstance(filename, str):
            raise RuntimeError('upgrade input has no local backing filename')
        path = Path(filename)
        if not path.is_absolute():
            path = (previous.parent if previous is not None else source.parent) / path
        path = canonical_path(path, existing=True)
        if index == 0 and path != source:
            raise RuntimeError('qemu-img inspected a different input image')
        if previous is not None:
            expected = images[index - 1].get('full-backing-filename')
            if not isinstance(expected, str) or canonical_path(Path(expected), existing=True) != path:
                raise RuntimeError('backing chain path changed during inspection')
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode):
            raise RuntimeError(f'upgrade input backing is not a regular local file: {path}')
        if any(item['path'] == str(path) for item in result):
            raise RuntimeError('upgrade input backing chain loops')
        result.append({'path': str(path), 'format': image['format'],
                       'device': info.st_dev, 'inode': info.st_ino,
                       'size': info.st_size, 'mtime_ns': info.st_mtime_ns,
                       'sha256': sha256(path), 'virtual_size': image.get('virtual-size'),
                       'actual_size': image.get('actual-size')})
        previous = path
    return result


def create_child(backing: Path, backing_format: str, child: Path) -> None:
    subprocess.run(['qemu-img', 'create', '-f', 'qcow2', '-F', backing_format,
                    '-b', str(backing), str(child)], check=True)


def flattened_backup(seed: Path, backup: Path) -> None:
    subprocess.run(['qemu-img', 'convert', '-f', 'qcow2', '-O', 'qcow2', '-c',
                    str(seed), str(backup)], check=True)
    info = image_info(backup)
    if (not isinstance(info, dict) or info.get('format') != 'qcow2'
            or info.get('backing-filename') or info.get('full-backing-filename')):
        raise RuntimeError('stopped backup is not an independent QCOW2 image')
    subprocess.run(['qemu-img', 'compare', '-f', 'qcow2', '-F', 'qcow2',
                    str(seed), str(backup)], check=True)


def load_boot_harness():
    path = Path(__file__).with_name('smoke-guest-upgrade.py')
    spec = importlib.util.spec_from_file_location('guest_upgrade', path)
    if spec is None or spec.loader is None:
        raise RuntimeError('cannot load guest upgrade boot harness')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.FIXTURES = FIXTURES
    return module


def image_sizes(work: Path) -> dict:
    sizes = {}
    for path in sorted(work.glob('*.qcow2')):
        file_info = path.stat()
        record = {'physical_bytes': file_info.st_blocks * 512,
                  'file_bytes': file_info.st_size}
        try:
            info = image_info(path)
            record.update({'virtual_bytes': info.get('virtual-size'),
                           'qemu_actual_bytes': info.get('actual-size')})
        except (OSError, subprocess.CalledProcessError, ValueError, TypeError) as error:
            # A failed conversion can leave a truncated image. Keep reporting
            # other images and, crucially, still recheck the immutable source.
            record['inspection_error'] = f'{type(error).__name__}: {error}'
        sizes[path.name] = record
    return sizes


def validate_paths(candidate: Path, source: Path, work: Path, chain: list[dict]) -> None:
    if not candidate.is_dir() or work.exists() or not work.parent.is_dir():
        raise ValueError('candidate must exist and work directory must be new')
    if any(',' in str(path) for path in (candidate, source, work, FIXTURES)):
        raise ValueError('QEMU paths must not contain commas')
    for item in chain:
        backing = Path(item['path'])
        if (work == backing or work.is_relative_to(backing) or backing.is_relative_to(work)
                or work == backing.parent):
            raise ValueError('work directory overlaps the existing upgrade disk chain')
    if (work == FIXTURES or work.is_relative_to(FIXTURES)
            or work == candidate or work.is_relative_to(candidate) or candidate.is_relative_to(work)
            or source.is_relative_to(candidate) or candidate.is_relative_to(source)):
        raise ValueError('candidate, upgrade input, fixtures, and work directory must be separate')


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('candidate', type=Path, help='same-run candidate build-spec, kernel, and initramfs')
    parser.add_argument('upgrade_disk', type=Path, help='stopped completed-upgrade disposable disk')
    parser.add_argument('work', type=Path, help='new directory for test-owned images and logs')
    parser.add_argument('--source-format', choices=('qcow2', 'raw'), default='qcow2')
    parser.add_argument('--timeout', type=int, default=1800, help='seconds per boot')
    args = parser.parse_args()
    if not os.access('/dev/kvm', os.R_OK | os.W_OK):
        parser.error('accessible /dev/kvm is required')
    if args.timeout <= 0:
        parser.error('timeout must be positive')
    candidate = canonical_path(args.candidate, existing=True)
    source = canonical_path(args.upgrade_disk, existing=True)
    work = canonical_path(args.work, existing=False)
    for name in ('vmlinuz-linux', 'initramfs-linux.img', 'build-spec.json'):
        if not (candidate / name).is_file():
            parser.error(f'missing candidate artifact: {name}')
    before = source_chain(source, args.source_format)
    validate_paths(candidate, source, work, before)
    free_before = shutil.disk_usage(work.parent).free
    if free_before < MIN_FREE_BYTES:
        parser.error(f'at least {MIN_FREE_BYTES // 1024**3} GiB free is required before the backup test')
    if not (FIXTURES / 'pause-write.c').is_file():
        parser.error('package-write interposer source is missing')

    work.mkdir(mode=0o700)
    report = {'schema': 1, 'passed': False, 'source_chain_before': before,
              'free_before_bytes': free_before, 'phases_completed': []}
    backup = work / 'pre-cut-backup.qcow2'
    backup_digest = None
    seed_digest = None
    primary_error = None
    try:
        preload = work / 'preload'
        preload.mkdir(mode=0o700)
        subprocess.run(['cc', '-shared', '-fPIC', '-O2', '-Wall', '-Wextra',
                        '-o', str(preload / 'pause-write.so'), str(FIXTURES / 'pause-write.c')], check=True)
        harness = load_boot_harness()
        seed = work / 'seed.qcow2'
        create_child(source, args.source_format, seed)
        harness.boot(candidate, seed, 'qcow2', 'seed', work / '01-seed.log', {}, args.timeout)
        report['phases_completed'].append('seed')
        flattened_backup(seed, backup)
        seed_digest = sha256(seed)
        backup_digest = sha256(backup)
        report['independent_backup_sha256'] = backup_digest

        fault = work / 'fault.qcow2'
        create_child(backup, 'qcow2', fault)
        nonce = secrets.token_hex(16)
        report['fault_nonce'] = nonce
        harness.boot(candidate, fault, 'qcow2', 'cut', work / '02-cut.log',
                     {'PACKAGE_WRITE_NONCE': nonce,
                      'PACKAGE_WRITE_PRELOAD': '/mnt/preload/pause-write.so'},
                     args.timeout, fault_cut_nonce=nonce, preload_dir=preload)
        report['phases_completed'].append('cut')
        harness.boot(candidate, fault, 'qcow2', 'torn', work / '03-torn.log', {}, args.timeout)
        report['phases_completed'].append('torn')

        restored = work / 'restored.qcow2'
        create_child(backup, 'qcow2', restored)
        harness.boot(candidate, restored, 'qcow2', 'restore', work / '04-restore.log', {}, args.timeout)
        report['phases_completed'].append('restore')
        harness.boot(candidate, restored, 'qcow2', 'reboot', work / '05-reboot.log', {}, args.timeout)
        report['phases_completed'].append('reboot')
    except Exception as error:
        primary_error = error
        report['error'] = f'{type(error).__name__}: {error}'
        raise
    finally:
        verification_error = None
        try:
            report['free_after_bytes'] = shutil.disk_usage(work).free
        except Exception as error:
            report['free_after_error'] = f'{type(error).__name__}: {error}'
            verification_error = error
        try:
            report['images'] = image_sizes(work)
            report['source_chain_after'] = source_chain(source, args.source_format)
            if report['source_chain_after'] != before:
                raise RuntimeError('completed upgrade source or backing changed during test')
            if backup_digest is not None and sha256(backup) != backup_digest:
                raise RuntimeError('independent stopped backup changed during test')
            if seed_digest is not None and sha256(work / 'seed.qcow2') != seed_digest:
                raise RuntimeError('stopped seed image changed during test')
        except Exception as error:
            report['passed'] = False
            report['verification_error'] = f'{type(error).__name__}: {error}'
            verification_error = error
        finally:
            if (verification_error is None and primary_error is None
                    and report['phases_completed'] == ['seed', 'cut', 'torn', 'restore', 'reboot']):
                report['passed'] = True
            (work / 'result.json').write_text(json.dumps(report, indent=2, sort_keys=True) + '\n')
        if verification_error is not None and primary_error is None:
            raise verification_error
    print(f'Package-write interruption and offline restore passed. Evidence: {work}', flush=True)


if __name__ == '__main__':
    main()
