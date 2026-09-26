#!/usr/bin/env python3
"""Exercise package-update interruption in a new disposable copy of a verified image."""
import argparse
import importlib.util
import os
from pathlib import Path
import subprocess


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('artifacts', type=Path, help='verified release artifacts including rootfs.ext4')
    parser.add_argument('work', type=Path, help='new evidence directory; must not exist')
    parser.add_argument('--timeout', type=int, default=1800, help='seconds per boot')
    args = parser.parse_args()
    artifacts, work = args.artifacts.resolve(), args.work.resolve()
    fixtures = Path(__file__).resolve().parent / 'package-recovery'
    if not os.access('/dev/kvm', os.R_OK | os.W_OK):
        parser.error('accessible /dev/kvm is required')
    for name in ('rootfs.ext4', 'vmlinuz-linux', 'initramfs-linux.img', 'build-spec.json'):
        if not (artifacts / name).is_file():
            parser.error(f'missing artifact: {name}')
    if any(',' in str(p) for p in (artifacts, work, fixtures)):
        parser.error('QEMU paths must not contain commas')
    if args.timeout <= 0:
        parser.error('timeout must be positive')
    work.mkdir(parents=True, exist_ok=False)
    disk = work / 'disposable.ext4'
    subprocess.run(['cp', '--reflink=auto', '--sparse=always', str(artifacts / 'rootfs.ext4'), str(disk)], check=True)
    with disk.open('r+b') as stream:
        stream.truncate(max(disk.stat().st_size, 24 * 1024**3))
    spec = importlib.util.spec_from_file_location('guest_upgrade', fixtures.parent / 'smoke-guest-upgrade.py')
    harness = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(harness)
    harness.FIXTURES = fixtures
    for index, phase in enumerate(('fresh', 'interrupt', 'recover', 'reboot'), 1):
        harness.boot(artifacts, disk, 'raw', phase, work / f'{index:02}-{phase}.log', {}, args.timeout)
    print(f'Package recovery passed; retained disk and logs: {work}')


if __name__ == '__main__':
    main()
