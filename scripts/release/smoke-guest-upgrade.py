#!/usr/bin/env python3
"""Upgrade a disposable copy of an older image and verify it across reboots."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import posixpath
import re
import selectors
import shlex
import signal
import subprocess
import time


FIXTURES = Path(__file__).resolve().parent / 'guest-upgrade'


def source_smoke_guest():
    """Share the fresh-image revision and native behavior probe."""
    path = Path(__file__).with_name('smoke-guest.py')
    spec = importlib.util.spec_from_file_location('smoke_guest', path)
    if spec is None or spec.loader is None:
        raise RuntimeError('cannot load guest smoke revision')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def source_compat_revision():
    """Use the same source revision expectation as the fresh-image smoke."""
    return source_smoke_guest().guest_compat_revision()


def candidate_checks(revision, compat_path='/usr/share/try-omarchy/compat-version',
                     bin_dir='/usr/local/bin'):
    """Run only with the candidate kernel/initramfs, before package updates."""
    checks = [f'test "$(cat {shlex.quote(compat_path)})" = "{revision}:$(uname -r)"']
    if revision >= 34:
        native = posixpath.join(bin_dir, 'omarchy-windows-native')
        checks.append(f'test -x {shlex.quote(native)}')
        checks.append(f'PYTHONDONTWRITEBYTECODE=1 PYTHONPATH={shlex.quote(bin_dir)} '
                      f'python3 {shlex.quote(native)} --help >/dev/null 2>&1')
    if revision >= 35:
        probe = ('from omarchy_windows_native_layout import occlusion_limit; '
                 'assert occlusion_limit({"protocol":1,"mode":"native",'
                 '"capabilities":{"occlusions":{"maxRectsPerWindow":16,'
                 '"coordinates":"tile"}}}) == 16')
        checks.append(f'PYTHONDONTWRITEBYTECODE=1 PYTHONPATH={shlex.quote(bin_dir)} '
                      f'python3 -c {shlex.quote(probe)}')
    if revision >= 37:
        checks.append(source_smoke_guest().native_fullscreen_workspace_probe(bin_dir))
    if revision >= 38:
        checks.append(source_smoke_guest().native_lua_dispatch_probe(bin_dir))
    if revision >= 40:
        checks.extend((
            'test -x /usr/bin/omarchy-update',
            'test -x /usr/bin/omarchy-channel-set',
            'test -x /usr/bin/omarchy-update-available',
            'test -x /usr/local/lib/try-omarchy/update-gate',
            'test -x /usr/share/try-omarchy/upstream-commands/omarchy-update',
        ))
    if revision >= 41:
        checks.extend((
            "grep -Fqx 'Exec=/usr/local/bin/omarchy-windows-open explorer' "
            "/usr/share/applications/omarchy-windows-explorer.desktop",
            "grep -Fqx 'Exec=/usr/local/bin/omarchy-windows-open league' "
            "/usr/share/applications/omarchy-windows-league.desktop",
            "grep -Fq 'matching_new' /usr/local/bin/omarchy-windows-open",
            "grep -Fq 'self.mapped = False' /usr/local/bin/omarchy-windows-native",
        ))
    return ' && '.join(checks)


def sha256(path):
    digest = hashlib.sha256()
    with path.open('rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(chunk)
    return digest.hexdigest()


def create_disk(baseline_disk, work, mode):
    """Keep the verified factory immutable; all five boots share one writable disk."""
    if mode == 'qcow2':
        disk = work / 'persistent.qcow2'
        size = max(baseline_disk.stat().st_size, 24 * 1024**3)
        subprocess.run(['qemu-img', 'create', '-f', 'qcow2', '-F', 'raw',
                        '-b', str(baseline_disk), str(disk), str(size)], check=True)
        return disk, 'qcow2'
    disk = work / 'persistent.ext4'
    subprocess.run(['cp', '--reflink=auto', '--sparse=always', str(baseline_disk), str(disk)], check=True)
    with disk.open('r+b') as stream:
        stream.truncate(max(disk.stat().st_size, 24 * 1024**3))
    return disk, 'raw'


def qemu_command(artifacts, disk, disk_format):
    spec = json.loads((artifacts / 'build-spec.json').read_text())
    cmdline = spec['runtime']['kernelCommandLine'].replace('console=tty0 ', '').replace('console=hvc0', 'console=ttyS0')
    cmdline += ' tryomarchy.instant=1 systemd.unit=multi-user.target'
    return [
        'qemu-system-x86_64', '-nodefaults', '-no-reboot', '-accel', 'kvm',
        '-machine', 'q35', '-cpu', 'host', '-smp', '4', '-m', '4096',
        '-display', 'none', '-monitor', 'none', '-serial', 'stdio',
        '-drive', f'file={disk},format={disk_format},if=virtio',
        '-kernel', str(artifacts / 'vmlinuz-linux'),
        '-initrd', str(artifacts / 'initramfs-linux.img'), '-append', cmdline,
        '-device', 'virtio-rng-pci', '-netdev', 'user,id=net0',
        '-device', 'virtio-net-pci,netdev=net0',
        '-fsdev', f'local,id=tests,path={FIXTURES},security_model=none,readonly=on',
        '-device', 'virtio-9p-pci,fsdev=tests,mount_tag=hostshare',
    ]


def completed_cut_line(transcript, scan_from, marker):
    """Match a whole serial line; terminal echo or partial output is not proof."""
    while (end := transcript.find(b'\n', scan_from)) != -1:
        line = bytes(transcript[scan_from:end]).rstrip(b'\r')
        scan_from = end + 1
        if line == marker:
            return True, scan_from
    return False, scan_from


def boot(artifacts, disk, disk_format, phase, log, environment, timeout, checks='',
         fault_cut_nonce=None, preload_dir=None):
    if fault_cut_nonce is not None:
        if not re.fullmatch(r'[0-9a-f]{32}', fault_cut_nonce) or environment.get('PACKAGE_WRITE_NONCE') != fault_cut_nonce:
            raise ValueError('fault cut requires the same 32-hex nonce in the guest environment')
        marker = f'PACKAGE_WRITE_CUT_READY:{fault_cut_nonce}'.encode('ascii')
    else:
        marker = None
    command = qemu_command(artifacts, disk, disk_format)
    if preload_dir is not None:
        preload_dir = Path(preload_dir)
        if not preload_dir.is_absolute() or not preload_dir.is_dir() or ',' in str(preload_dir):
            raise ValueError('preload directory must be a real absolute QEMU-safe directory')
        command += ['-fsdev', f'local,id=preload,path={preload_dir},security_model=none,readonly=on',
                    '-device', 'virtio-9p-pci,fsdev=preload,mount_tag=preload']
    process = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=0)
    selector = selectors.DefaultSelector()
    selector.register(process.stdout, selectors.EVENT_READ)
    transcript = bytearray()
    login = password = -1
    sent = False
    password_at = None
    scan_from = 0
    cut_seen = False
    deadline = time.monotonic() + timeout
    try:
        with log.open('wb') as output:
            while time.monotonic() < deadline:
                for key, _ in selector.select(timeout=1):
                    data = os.read(key.fileobj.fileno(), 65536)
                    if not data:
                        continue
                    output.write(data)
                    output.flush()
                    transcript.extend(data)
                    if marker is not None:
                        found, scan_from = completed_cut_line(transcript, scan_from, marker)
                        if found:
                            if not sent:
                                raise RuntimeError(f'fault cut marker arrived before fixture launch; see {log}')
                            # The fixture has stopped pacman, verified the
                            # partial write and synced before this line.
                            # Crash only the QEMU child opened by this boot.
                            process.kill()
                            process.wait(timeout=30)
                            if process.returncode != -signal.SIGKILL:
                                raise RuntimeError(f'fault cut did not SIGKILL this QEMU; see {log}')
                            cut_seen = True
                        if cut_seen:
                            break
                    pos = transcript.rfind(b'login:')
                    if not sent and pos > login:
                        process.stdin.write(b'omarchy\n')
                        process.stdin.flush()
                        login = pos
                    pos = transcript.rfind(b'Password:')
                    if not sent and pos > password:
                        process.stdin.write(b'omarchy\n')
                        process.stdin.flush()
                        password = pos
                        password_at = time.monotonic()
                if password_at is not None and not sent and time.monotonic() - password_at > 3:
                    # One short command avoids terminal input limits and sudo
                    # discarding queued lines. Test scripts mount read-only.
                    assignments = ' '.join(shlex.quote(f'{k}={v}') for k, v in environment.items())
                    script = f'{checks} && ' if checks else ''
                    script += f'env {assignments} bash /mnt/host/{phase}.sh; result=$?; '
                    script += f"printf 'UPGRADE_%s:%s:%s\\n' RESULT {phase} $result; sudo systemctl poweroff\n"
                    process.stdin.write(script.encode())
                    process.stdin.flush()
                    sent = True
                if process.poll() is not None:
                    break
            else:
                raise RuntimeError(f'guest test timed out; see {log}')
        if marker is not None:
            if not cut_seen:
                raise RuntimeError(f'fault cut marker was not observed; see {log}')
        elif process.returncode != 0 or f'UPGRADE_RESULT:{phase}:0'.encode() not in transcript:
            raise RuntimeError(f'guest test failed; see {log}')
    finally:
        selector.close()
        if process.poll() is None:
            process.terminate()
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
        process.stdin.close()
        process.stdout.close()
    print(f'PASS {log.stem}', flush=True)


def runtime(spec):
    upstream = spec['upstream']
    return f"{upstream['version']}-{upstream.get('packageRelease', 1)}"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('baseline', type=Path, help='verified older release artifacts, including decompressed rootfs.ext4')
    parser.add_argument('candidate', type=Path, help='newly built candidate artifacts')
    parser.add_argument('work', type=Path, help='new directory for the disposable disk and logs; must not exist')
    parser.add_argument('--disk-mode', choices=('qcow2', 'raw'), default='qcow2',
                        help='writable backing overlay by default; raw preserves the older copy mode')
    parser.add_argument('--candidate-compat-revision', type=int, default=None,
                        help='expected candidate guest revision; defaults to the current source patch revision')
    parser.add_argument('--timeout', type=int, default=1800, help='seconds per boot')
    args = parser.parse_args()
    if not os.access('/dev/kvm', os.R_OK | os.W_OK):
        parser.error('accessible /dev/kvm is required')
    revision = args.candidate_compat_revision if args.candidate_compat_revision is not None else source_compat_revision()
    if not 1 <= revision <= 999999:
        parser.error('candidate compatibility revision is invalid')
    baseline, candidate, work = (p.resolve() for p in (args.baseline, args.candidate, args.work))
    if (baseline == candidate or work.is_relative_to(baseline) or work.is_relative_to(candidate)
            or baseline.is_relative_to(work) or candidate.is_relative_to(work)):
        parser.error('baseline, candidate, and disposable work directory must be separate')
    for directory in (baseline, candidate):
        # Only the baseline factory is copied. Candidate boots reuse that disk.
        required = ('vmlinuz-linux', 'initramfs-linux.img', 'build-spec.json')
        if directory == baseline:
            required += ('rootfs.ext4',)
        for filename in required:
            if not (directory / filename).is_file():
                parser.error(f'missing artifact: {directory / filename}')
        if ',' in str(directory):
            parser.error('QEMU paths must not contain commas')
    if ',' in str(work) or ',' in str(FIXTURES):
        parser.error('QEMU paths must not contain commas')
    specs = [json.loads((p / 'build-spec.json').read_text()) for p in (baseline, candidate)]
    environment = {'BASELINE_RUNTIME': runtime(specs[0]), 'CANDIDATE_RUNTIME': runtime(specs[1]),
                   'CANDIDATE_VERSION': specs[1]['upstream']['version']}
    work.mkdir(parents=True, exist_ok=False)
    baseline_disk = baseline / 'rootfs.ext4'
    baseline_digest = sha256(baseline_disk)
    try:
        disk, disk_format = create_disk(baseline_disk, work, args.disk_mode)
        for artifacts, phase, name in (
            (baseline, 'seed', '01-seed'), (candidate, 'upgrade', '02-upgrade'),
            (candidate, 'reboot', '03-reboot'), (baseline, 'reboot', '04-backward-image'),
            (candidate, 'reboot', '05-return-to-candidate'),
        ):
            checks = candidate_checks(revision) if artifacts == candidate else ''
            boot(artifacts, disk, disk_format, phase, work / f'{name}.log', environment, args.timeout, checks)
    finally:
        if sha256(baseline_disk) != baseline_digest:
            raise RuntimeError('verified baseline factory disk changed during upgrade smoke')
    print(f'Upgrade and preservation checks passed. Evidence: {work}')


if __name__ == '__main__':
    main()
