#!/usr/bin/env python3
"""Ensure the patched image ships the current Windows integration sources."""
import argparse
from pathlib import Path


def verify(source: Path, guest: Path) -> list[str]:
    binaries = sorted(set(source.glob('omarchy-windows-*')) |
                      set(source.glob('omarchy_windows_*.py')))
    if not binaries:
        raise ValueError('no Windows integration sources found')
    pairs = [(path, guest / 'factory-overlay/usr/local/bin' / path.name)
             for path in binaries]
    pairs.extend((source / name, guest / target) for name, target in (
        ('export-seamless-token',
         'factory-overlay/usr/local/lib/try-omarchy/export-seamless-token'),
        ('try-omarchy-seamless-token.service',
         'factory-overlay/etc/systemd/system/try-omarchy-seamless-token.service'),
    ))
    failures = []
    for original, packaged in pairs:
        if not original.is_file() or not packaged.is_file():
            failures.append(f'{original.name}: source or packaged file missing')
        elif original.read_bytes().replace(b'\r\n', b'\n') != packaged.read_bytes().replace(b'\r\n', b'\n'):
            failures.append(f'{original.name}: packaged content differs; add a guest-build patch')
    return failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('guest', type=Path, help='patched checkout guest directory')
    parser.add_argument('--source', type=Path,
                        default=Path(__file__).resolve().parents[1] / 'windows-apps')
    args = parser.parse_args()
    failures = verify(args.source, args.guest)
    if failures:
        raise SystemExit('\n'.join(failures))
    print('Packaged Windows integration matches development sources')


if __name__ == '__main__':
    main()
