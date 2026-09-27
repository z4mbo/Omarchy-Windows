"""Opt-in, disposable Windows WHPX/QMP startup timing diagnostic.

The caller supplies an inactive guest disk. QEMU always uses -snapshot, and
this script never starts the installed Omarchy launcher or touches its QMP.
"""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import subprocess
import time


QEMU_SHA256 = "43160f86cbf28a67df6f529b104dd03f63a37ca5d3d2a9148f11358fbc559b0a"
QEMU64 = "qemu64,+ssse3,+sse4.1,+sse4.2,+popcnt,+aes"


def free_port():
    with socket.socket() as reservation:
        reservation.bind(("127.0.0.1", 0))
        return reservation.getsockname()[1]


def qmp_readline(connection):
    data = bytearray()
    while not data.endswith(b"\n"):
        part = connection.recv(1)
        if not part:
            raise ConnectionError("QMP closed before newline")
        data.extend(part)
        if len(data) > 1024 * 1024:
            raise ValueError("QMP line too long")
    return json.loads(data)


def qmp_call(connection, command):
    connection.sendall(json.dumps({"execute": command, "id": command}).encode() + b"\n")
    while True:
        reply = qmp_readline(connection)
        if "event" in reply:
            continue
        if reply.get("id") != command or "error" in reply:
            raise RuntimeError(f"QMP {command}: {reply}")
        return reply["return"]


def ssh_probe(port, key, remote="echo ready"):
    result = subprocess.run(
        ["ssh.exe", "-p", str(port), "-i", str(key), "-o", "BatchMode=yes",
         "-o", "LogLevel=ERROR", "-o", "StrictHostKeyChecking=no",
         "-o", "UserKnownHostsFile=NUL", "-o", "ConnectTimeout=2",
         "z4mbo@127.0.0.1", remote],
        capture_output=True, text=True, timeout=5,
    )
    return result.returncode == 0 and "ready" in result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("qemu", "disk", "kernel", "initrd", "ssh-key", "out"):
        parser.add_argument("--" + name, required=True, type=Path)
    parser.add_argument("--cpu", choices=("host", "qemu64"), required=True)
    parser.add_argument("--display", choices=("none", "vnc"), required=True)
    parser.add_argument("--connect", choices=("early", "after-ssh"), required=True)
    parser.add_argument("--first-qmp-after-s", type=float, default=0,
                        help="for after-ssh, delay the first QMP connection to this time after launch")
    parser.add_argument("--second-client-probe", action="store_true",
                        help="probe whether a second TCP client gets a greeting while the first is open")
    args = parser.parse_args()
    for name in ("qemu", "disk", "kernel", "initrd", "ssh_key"):
        path = getattr(args, name)
        if not path.is_absolute() or not path.is_file():
            parser.error(f"--{name.replace('_', '-')} must name an existing absolute file")
    if "\\dist\\acceptance\\vm\\disk.raw" not in str(args.disk).lower():
        parser.error("this diagnostic accepts only the inactive acceptance disk")
    if args.out.exists() or not args.out.is_absolute():
        parser.error("--out must be a new absolute directory")
    if hashlib.sha256(args.qemu.read_bytes()).hexdigest() != QEMU_SHA256:
        parser.error("QEMU SHA-256 differs from the verified experimental runtime")
    args.out.mkdir(parents=True)
    result = {"cpu": args.cpu, "display": args.display, "connect": args.connect,
              "first_qmp_after_s": args.first_qmp_after_s,
              "qemu_sha256": QEMU_SHA256, "snapshot": True}
    qmp_port, ssh_port = free_port(), free_port()
    serial_path = args.out / "serial.log"
    stderr_path = args.out / "qemu-stderr.log"
    public_key = Path(str(args.ssh_key) + ".pub").read_bytes().strip()
    command = [str(args.qemu), "-machine", "q35,accel=whpx", "-cpu",
               "host" if args.cpu == "host" else QEMU64,
               "-smp", "4", "-m", "4096", "-nodefaults", "-serial", f"file:{serial_path}",
               "-device", "virtio-balloon-pci,id=experimental-balloon",
               "-qmp", f"tcp:127.0.0.1:{qmp_port},server=on,wait=off",
               "-snapshot", "-drive", f"file={args.disk},format=raw,if=virtio",
               "-kernel", str(args.kernel), "-initrd", str(args.initrd),
               "-append", "root=/dev/vda rw rootwait console=ttyS0 loglevel=4 "
                          "tryomarchy.sshd=1 tryomarchy.sshkey=" + base64.b64encode(public_key).decode(),
               "-netdev", f"user,id=n0,hostfwd=tcp:127.0.0.1:{ssh_port}-:22",
               "-device", "virtio-net-pci,netdev=n0"]
    if args.display == "none":
        command += ["-display", "none"]
    else:
        for display_number in range(70, 100):
            with socket.socket() as reservation:
                try:
                    reservation.bind(("127.0.0.1", 5900 + display_number))
                    break
                except OSError:
                    continue
        else:
            parser.error("no free loopback VNC display from 70 through 99")
        command += ["-vga", "none", "-device", "virtio-gpu-pci",
                    "-display", f"vnc=127.0.0.1:{display_number}"]
        result["vnc_port"] = 5900 + display_number
    environment = os.environ.copy()
    environment["OMARCHY_QEMU_BALLOON_DECOMMIT"] = "1"
    started = time.monotonic()
    connection = None
    with stderr_path.open("wb") as stderr:
        child = subprocess.Popen(command, env=environment, stdout=subprocess.DEVNULL,
                                 stderr=stderr, creationflags=subprocess.CREATE_NO_WINDOW)
        result["pid"] = child.pid
        try:
            if args.connect == "after-ssh":
                deadline = started + 90
                while time.monotonic() < deadline and child.poll() is None:
                    try:
                        if ssh_probe(ssh_port, args.ssh_key):
                            result["ssh_ready_s"] = round(time.monotonic() - started, 2)
                            break
                    except (OSError, subprocess.TimeoutExpired):
                        pass
                    time.sleep(1)
                else:
                    result["ssh_ready"] = False
                while child.poll() is None and time.monotonic() - started < args.first_qmp_after_s:
                    time.sleep(.1)
                if "ssh_ready_s" in result:
                    try:
                        result["ssh_before_qmp"] = ssh_probe(ssh_port, args.ssh_key)
                    except (OSError, subprocess.TimeoutExpired):
                        result["ssh_before_qmp"] = False
            try:
                if args.connect == "early":
                    deadline = started + 15
                    while True:
                        try:
                            connection = socket.create_connection(("127.0.0.1", qmp_port), timeout=2)
                            break
                        except OSError:
                            if time.monotonic() >= deadline or child.poll() is not None:
                                raise
                            time.sleep(.1)
                else:
                    connection = socket.create_connection(("127.0.0.1", qmp_port), timeout=3)
                result["qmp_tcp_connected_s"] = round(time.monotonic() - started, 2)
                connection.settimeout(8)
                greeting = qmp_readline(connection)
                if "QMP" not in greeting:
                    raise ValueError(f"unexpected QMP greeting: {greeting}")
                result["qmp_greeting_s"] = round(time.monotonic() - started, 2)
                qmp_call(connection, "qmp_capabilities")
                result["qmp_capabilities_s"] = round(time.monotonic() - started, 2)
                result["qmp_status"] = qmp_call(connection, "query-status")
                if args.second_client_probe:
                    second = socket.create_connection(("127.0.0.1", qmp_port), timeout=3)
                    try:
                        second.settimeout(2)
                        try:
                            result["second_greeting_while_first_open"] = "QMP" in qmp_readline(second)
                        except (OSError, ConnectionError) as error:
                            result["second_while_first_open_error"] = f"{type(error).__name__}: {error}"
                        connection.close()
                        connection = None
                        second.settimeout(8)
                        if "second_greeting_while_first_open" not in result:
                            try:
                                result["second_greeting_after_first_close"] = "QMP" in qmp_readline(second)
                            except (OSError, ConnectionError) as error:
                                result["second_after_first_close_error"] = f"{type(error).__name__}: {error}"
                        if result.get("second_greeting_after_first_close") or result.get("second_greeting_while_first_open"):
                            qmp_call(second, "qmp_capabilities")
                            result["second_capabilities_after_first_close"] = True
                            connection = second
                    finally:
                        if connection is not second:
                            second.close()
            except (OSError, ConnectionError, ValueError, RuntimeError) as error:
                result["qmp_error"] = f"{type(error).__name__}: {error}"
            if args.connect == "early":
                deadline = started + 90
                while time.monotonic() < deadline and child.poll() is None:
                    try:
                        if ssh_probe(ssh_port, args.ssh_key):
                            result["ssh_ready_s"] = round(time.monotonic() - started, 2)
                            break
                    except (OSError, subprocess.TimeoutExpired):
                        pass
                    time.sleep(1)
                else:
                    result["ssh_ready"] = False
            elif "qmp_error" in result:
                try:
                    result["ssh_after_qmp_error"] = ssh_probe(ssh_port, args.ssh_key)
                except (OSError, subprocess.TimeoutExpired):
                    result["ssh_after_qmp_error"] = False
            result["qemu_running_after_probe"] = child.poll() is None
            if connection and "qmp_capabilities_s" in result and child.poll() is None:
                try:
                    qmp_call(connection, "system_powerdown")
                    result["acpi_powerdown_requested"] = True
                    child.wait(timeout=20)
                    result["clean_shutdown"] = child.returncode == 0
                except (OSError, RuntimeError, subprocess.TimeoutExpired) as error:
                    result["shutdown_error"] = f"{type(error).__name__}: {error}"
        finally:
            if connection:
                connection.close()
            if child.poll() is None:
                child.terminate()
                try:
                    child.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    child.kill()
                    child.wait(timeout=5)
                result["forced_disposable_stop"] = True
            result["qemu_exit_code"] = child.returncode
            result["duration_s"] = round(time.monotonic() - started, 2)
            result["serial_bytes"] = serial_path.stat().st_size if serial_path.exists() else 0
            result["qemu_stderr_bytes"] = stderr_path.stat().st_size
            (args.out / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
