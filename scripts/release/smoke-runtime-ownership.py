#!/usr/bin/env python3
"""Test corrected runtime packaging and upgrade a disposable v18 guest."""
import argparse
import importlib.util
import os
from pathlib import Path
import subprocess
import shutil


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('artifacts', type=Path, help='verified release artifacts including rootfs.ext4')
    parser.add_argument('builder', type=Path, help='guest builder with the complete candidate patch series applied')
    parser.add_argument('work', type=Path, help='new evidence directory; must not exist')
    parser.add_argument('--timeout', type=int, default=1800, help='seconds per boot')
    args = parser.parse_args()
    artifacts, work = args.artifacts.resolve(), args.work.resolve()
    fixture_source = Path(__file__).resolve().parent / 'runtime-ownership'
    fixtures = work / 'fixtures'
    builder = args.builder.resolve()
    for name in ('guest/scripts/register-omarchy-runtime.sh', 'guest/scripts/runtime-ownership.install', 'guest/spec.json'):
        if not (builder / name).is_file():
            parser.error(f'missing builder file: {name}')
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
    shutil.copytree(fixture_source, fixtures)
    shutil.copy2(builder / 'guest/scripts/register-omarchy-runtime.sh', fixtures)
    shutil.copy2(builder / 'guest/spec.json', fixtures)
    shutil.copy2(builder / 'guest/scripts/runtime-ownership.install', fixtures)
    disk = work / 'disposable.ext4'
    subprocess.run(['cp', '--reflink=auto', '--sparse=always', str(artifacts / 'rootfs.ext4'), str(disk)], check=True)
    with disk.open('r+b') as stream:
        stream.truncate(max(disk.stat().st_size, 24 * 1024**3))
    spec = importlib.util.spec_from_file_location('guest_upgrade', Path(__file__).resolve().parent / 'smoke-guest-upgrade.py')
    harness = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(harness)
    harness.FIXTURES = fixtures
    for index, phase in enumerate(('upgrade', 'reboot'), 1):
        harness.boot(artifacts, disk, 'raw', phase, work / f'{index:02}-{phase}.log', {}, args.timeout)
    print(f'Runtime ownership passed; retained disk and logs: {work}')


if __name__ == '__main__':
    main()
