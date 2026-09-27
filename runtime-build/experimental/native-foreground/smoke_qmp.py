#!/usr/bin/env python3
"""Confirm the isolated runtime exposes the bounded downstream QMP command."""

from __future__ import annotations

import json
from collections import deque
import queue
import subprocess
import sys
import threading
import time
from pathlib import Path


COMMAND = "__omarchy_native-foreground-handoff"


def smoke(qemu: Path) -> None:
    # QMP requires a greeting/capabilities exchange. Pipelining these requests
    # into stdio caused the Windows chardev to parse later input as fragments,
    # hiding query-commands even though the compiled handler was present.
    process = subprocess.Popen(
        [str(qemu), "-machine", "none", "-nodefaults", "-display", "none", "-S", "-qmp", "stdio"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
        creationflags=subprocess.CREATE_NO_WINDOW if sys.platform == "win32" else 0,
    )
    incoming: queue.Queue[str | None] = queue.Queue()
    stderr_tail: deque[str] = deque(maxlen=32)

    def collect_stdout() -> None:
        try:
            for line in process.stdout:
                incoming.put(line)
        finally:
            incoming.put(None)

    def collect_stderr() -> None:
        for line in process.stderr:
            stderr_tail.append(line)

    stdout_reader = threading.Thread(target=collect_stdout, daemon=True)
    stderr_reader = threading.Thread(target=collect_stderr, daemon=True)
    stdout_reader.start()
    stderr_reader.start()
    deadline = time.monotonic() + 30

    def reply(wanted: str) -> dict:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise RuntimeError(f"timed out awaiting QMP {wanted}: {''.join(stderr_tail)[-2000:]}")
            try:
                line = incoming.get(timeout=remaining)
            except queue.Empty as exc:
                raise RuntimeError(f"timed out awaiting QMP {wanted}: {''.join(stderr_tail)[-2000:]}") from exc
            if line is None:
                raise RuntimeError(f"QMP ended before {wanted}: {''.join(stderr_tail)[-2000:]}")
            message = json.loads(line)
            if wanted == "greeting" and "QMP" in message:
                return message
            if message.get("id") == wanted:
                return message
            if "id" in message or "error" in message:
                raise RuntimeError(f"unexpected QMP reply before {wanted}: {message}")

    def execute(command: str, ident: str, arguments: dict | None = None) -> dict:
        request = {"execute": command, "id": ident}
        if arguments is not None:
            request["arguments"] = arguments
        process.stdin.write(json.dumps(request, separators=(",", ":")) + "\n")
        process.stdin.flush()
        return reply(ident)

    try:
        reply("greeting")
        if "return" not in execute("qmp_capabilities", "capabilities"):
            raise RuntimeError("QMP capabilities negotiation failed")
        commands = execute("query-commands", "commands").get("return")
        arguments = {
            "hwnd": 1,
            "pid": 4294967295,
            "created": 1,
            "property": "Omarchy.Windows.Grant." + "0" * 32,
            "incarnation": 1,
            "expires": 1,
        }
        any_error = execute(COMMAND, "reject-any-pid", arguments).get("error", {})
        arguments["pid"] = 1
        display_error = execute(COMMAND, "reject-no-display", arguments).get("error", {})
        if "return" not in execute("quit", "quit"):
            raise RuntimeError("QMP quit failed")
        try:
            process.wait(timeout=max(0.1, deadline - time.monotonic()))
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError("QEMU did not exit after QMP quit") from exc
        if process.returncode != 0:
            raise RuntimeError(f"QEMU QMP smoke failed with exit {process.returncode}: {''.join(stderr_tail)[-2000:]}")
    finally:
        if process.poll() is None:
            process.kill()
            process.wait(timeout=5)
        stdout_reader.join(timeout=2)
        stderr_reader.join(timeout=2)
        process.stdin.close()
        if not stdout_reader.is_alive():
            process.stdout.close()
        if not stderr_reader.is_alive():
            process.stderr.close()

    if not isinstance(commands, list) or COMMAND not in {item.get("name") for item in commands}:
        raise RuntimeError("isolated runtime lacks native foreground QMP command")
    if any_error.get("desc") != "invalid native foreground handoff identity":
        raise RuntimeError("QMP command accepted ASFW_ANY instead of rejecting it")
    if display_error.get("desc") != "Omarchy SDL display is not the interactive foreground window":
        raise RuntimeError("QMP command did not reject a missing interactive SDL display")
    print("isolated native foreground QMP command rejects ASFW_ANY and an absent SDL display")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} QEMU_SYSTEM_X86_64_EXE")
    smoke(Path(sys.argv[1]))
