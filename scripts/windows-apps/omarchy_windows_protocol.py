"""Bounded guest client for Omarchy's authenticated Windows window bridge.

The Windows launcher serves this only on host loopback. QEMU user networking
maps that listener to 10.0.2.2 in the guest. The token is supplied by QEMU
fw_cfg on each launch and is never stored in the guest disk image.
"""

from __future__ import annotations

import json
from pathlib import Path
import struct
import urllib.error
import urllib.parse
import urllib.request

BASE_URL = "http://10.0.2.2:4457"
TOKEN_FILE = Path("/run/try-omarchy/seamless-token")
MAX_JSON = 1024 * 1024
MAX_PNG = 64 * 1024 * 1024
MAX_IMAGE_DIMENSION = 8192
PNG_SIGNATURE = b"\x89PNG\r\n\x1a\n"


class BridgeError(RuntimeError):
    """The Windows window bridge is absent or returned an invalid response."""


def load_token(path: Path = TOKEN_FILE) -> str:
    try:
        token = path.read_bytes().strip().decode("ascii")
    except (OSError, UnicodeDecodeError) as exc:
        raise BridgeError("The Omarchy window bridge token is unavailable. Restart with the updated Windows launcher and guest integration.") from exc
    if len(token) != 64 or any(c not in "0123456789abcdefABCDEF" for c in token):
        raise BridgeError("The Omarchy window bridge token is invalid. Restart with the updated Windows launcher.")
    return token


def validate_window(raw: object) -> dict:
    if not isinstance(raw, dict):
        raise BridgeError("The Windows bridge returned an invalid window entry.")
    ident = raw.get("id")
    if not isinstance(ident, str) or not 1 <= len(ident) <= 128 or any(ord(c) < 33 for c in ident):
        raise BridgeError("The Windows bridge returned an invalid window ID.")
    width, height = raw.get("width"), raw.get("height")
    if not isinstance(width, int) or not isinstance(height, int) or not 0 < width <= MAX_IMAGE_DIMENSION or not 0 < height <= MAX_IMAGE_DIMENSION:
        raise BridgeError("The Windows bridge returned an invalid window size.")
    title, process = raw.get("title", ""), raw.get("process", "")
    if not isinstance(title, str) or not isinstance(process, str):
        raise BridgeError("The Windows bridge returned invalid window metadata.")
    group = raw.get("appGroup")
    if group is not None and (not isinstance(group, str) or len(group) > 64):
        raise BridgeError("The Windows bridge returned an invalid application group.")
    return {
        "id": ident,
        "title": title[:200],
        "process": process[:100],
        "appGroup": group or process.lower(),
        "width": width,
        "height": height,
        "fullscreen": raw.get("fullscreen") is True,
    }


def validate_png(data: bytes) -> tuple[int, int]:
    if len(data) < 24 or len(data) > MAX_PNG or not data.startswith(PNG_SIGNATURE) or data[12:16] != b"IHDR":
        raise BridgeError("The Windows bridge returned an invalid PNG frame.")
    width, height = struct.unpack(">II", data[16:24])
    if not 0 < width <= MAX_IMAGE_DIMENSION or not 0 < height <= MAX_IMAGE_DIMENSION or width * height > 32_000_000:
        raise BridgeError("The Windows bridge returned an oversized frame.")
    return width, height


class Bridge:
    def __init__(self, token: str, base_url: str = BASE_URL):
        if len(token) != 64:
            raise ValueError("Invalid bridge token")
        self.token = token
        self.base_url = base_url.rstrip("/")
        # Never send this bearer token through an environment-configured proxy.
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def request(self, path: str, *, payload: dict | None = None, limit: int = MAX_JSON, timeout: float = 2) -> bytes:
        body = None if payload is None else json.dumps(payload, separators=(",", ":")).encode("utf-8")
        request = urllib.request.Request(
            self.base_url + path,
            data=body,
            method="GET" if body is None else "POST",
            headers={
                "Authorization": f"Bearer {self.token}",
                "Accept": "application/json" if limit == MAX_JSON else "image/png",
                **({"Content-Type": "application/json"} if body is not None else {}),
            },
        )
        try:
            with self.opener.open(request, timeout=timeout) as response:
                data = response.read(limit + 1)
                if len(data) > limit:
                    raise BridgeError("The Windows bridge response exceeded its size limit.")
                return data
        except urllib.error.HTTPError as exc:
            if exc.code == 401:
                raise BridgeError("The Windows bridge rejected its launch token. Restart Omarchy.") from exc
            if exc.code == 404:
                raise BridgeError("The Windows window has closed.") from exc
            raise BridgeError(f"The Windows bridge returned HTTP {exc.code}.") from exc
        except (urllib.error.URLError, TimeoutError, OSError) as exc:
            raise BridgeError("The Windows window bridge is unavailable. Start the updated Omarchy Windows launcher.") from exc

    @staticmethod
    def window_path(ident: str) -> str:
        return "/v1/windows/" + urllib.parse.quote(ident, safe="")

    def windows(self) -> list[dict]:
        try:
            document = json.loads(self.request("/v1/windows"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise BridgeError("The Windows bridge returned invalid window data.") from exc
        raw = document.get("windows") if isinstance(document, dict) else None
        if not isinstance(raw, list) or len(raw) > 64:
            raise BridgeError("The Windows bridge returned an invalid window list.")
        return [validate_window(item) for item in raw]

    def frame(self, ident: str) -> tuple[bytes, tuple[int, int]]:
        image = self.request(self.window_path(ident) + "/frame", limit=MAX_PNG, timeout=6)
        return image, validate_png(image)

    def input(self, ident: str, payload: dict) -> None:
        self.request(self.window_path(ident) + "/input", payload=payload)

    def close(self, ident: str) -> None:
        self.request(self.window_path(ident) + "/close", payload={})
