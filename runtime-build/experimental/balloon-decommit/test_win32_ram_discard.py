#!/usr/bin/env python3
"""Small native Windows fixture for the decommit/recommit API contract."""

from __future__ import annotations

import ctypes
import mmap
import os


MEM_COMMIT = 0x1000
MEM_RESERVE = 0x2000
MEM_DECOMMIT = 0x4000
MEM_RELEASE = 0x8000
PAGE_READWRITE = 0x04


def main() -> None:
    if os.name != "nt":
        raise SystemExit("this fixture requires Windows")

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.VirtualAlloc.argtypes = (
        ctypes.c_void_p,
        ctypes.c_size_t,
        ctypes.c_ulong,
        ctypes.c_ulong,
    )
    kernel32.VirtualAlloc.restype = ctypes.c_void_p
    kernel32.VirtualFree.argtypes = (ctypes.c_void_p, ctypes.c_size_t, ctypes.c_ulong)
    kernel32.VirtualFree.restype = ctypes.c_int

    page = mmap.PAGESIZE
    size = page * 2
    address = kernel32.VirtualAlloc(None, size, MEM_RESERVE | MEM_COMMIT, PAGE_READWRITE)
    if not address:
        raise OSError(ctypes.get_last_error(), "initial VirtualAlloc failed")

    try:
        ctypes.memset(address, 0xA5, page)
        ctypes.memset(address + page, 0x5A, page)
        if not kernel32.VirtualFree(address, page, MEM_DECOMMIT):
            raise OSError(ctypes.get_last_error(), "MEM_DECOMMIT failed")
        restored = kernel32.VirtualAlloc(address, page, MEM_COMMIT, PAGE_READWRITE)
        if restored != address:
            raise OSError(ctypes.get_last_error(), "same-address MEM_COMMIT failed")
        if ctypes.string_at(address, page) != bytes(page):
            raise AssertionError("recommitted page did not read as zero")
        if ctypes.string_at(address + page, page) != b"\x5a" * page:
            raise AssertionError("adjacent committed page changed")
    finally:
        if not kernel32.VirtualFree(address, 0, MEM_RELEASE):
            raise OSError(ctypes.get_last_error(), "MEM_RELEASE failed")

    print("ok - Windows same-address recommit returns zeroed pages and preserves neighbors")


if __name__ == "__main__":
    main()
