#!/usr/bin/env python3
"""Confirm the isolated runtime exposes the bounded downstream QMP command."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path


COMMAND = "__omarchy_native_foreground_handoff"


def smoke(qemu: Path) -> None:
    requests = [
        {"execute": "qmp_capabilities", "id": "capabilities"},
        {"execute": "query-commands", "id": "commands"},
        {
            "execute": COMMAND,
            "arguments": {
                "hwnd": 1,
                "pid": 4294967295,
                "created": 1,
                "property": "Omarchy.Windows.Grant." + "0" * 32,
                "incarnation": 1,
                "expires": 1,
            },
            "id": "reject-any-pid",
        },
        {
            "execute": COMMAND,
            "arguments": {
                "hwnd": 1,
                "pid": 1,
                "created": 1,
                "property": "Omarchy.Windows.Grant." + "0" * 32,
                "incarnation": 1,
                "expires": 1,
            },
            "id": "reject-no-display",
        },
        {"execute": "quit", "id": "quit"},
    ]
    result = subprocess.run(
        [str(qemu), "-machine", "none", "-nodefaults", "-display", "none", "-S", "-qmp", "stdio"],
        input="".join(json.dumps(item, separators=(",", ":")) + "\n" for item in requests),
        text=True,
        capture_output=True,
        timeout=30,
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"QEMU QMP smoke failed with exit {result.returncode}: {result.stderr[-2000:]}")
    messages = [json.loads(line) for line in result.stdout.splitlines() if line.startswith("{")]
    replies = {message["id"]: message for message in messages if "id" in message}
    if "return" not in replies.get("capabilities", {}):
        raise RuntimeError("QMP capabilities negotiation failed")
    commands = replies.get("commands", {}).get("return")
    if not isinstance(commands, list) or COMMAND not in {item.get("name") for item in commands}:
        raise RuntimeError("isolated runtime lacks native foreground QMP command")
    any_error = replies.get("reject-any-pid", {}).get("error", {})
    if any_error.get("desc") != "invalid native foreground handoff identity":
        raise RuntimeError("QMP command accepted ASFW_ANY instead of rejecting it")
    display_error = replies.get("reject-no-display", {}).get("error", {})
    if display_error.get("desc") != "Omarchy SDL display is not the interactive foreground window":
        raise RuntimeError("QMP command did not reject a missing interactive SDL display")
    print("isolated native foreground QMP command rejects ASFW_ANY and an absent SDL display")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit(f"usage: {sys.argv[0]} QEMU_SYSTEM_X86_64_EXE")
    smoke(Path(sys.argv[1]))
