"""Pure, fail-closed Hyprland-to-host layout translation for native projection."""

from __future__ import annotations

import hashlib
import math
import re

MAX_WINDOWS = 8
MAX_DIMENSION = 8192
WINDOW_ID = re.compile(r"[0-9a-f]{32}\Z")


class LayoutError(ValueError):
    """Hyprland state cannot be mapped safely onto the host output."""


def _integer(value: object) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def marker_for(ident: str) -> str:
    if not isinstance(ident, str) or not WINDOW_ID.fullmatch(ident):
        raise LayoutError("The host provided an invalid native window ID.")
    return f" [Omarchy Native {hashlib.sha256(ident.encode('ascii')).hexdigest()[:16]}]"


def parse_presentation(document: object) -> None:
    if not isinstance(document, dict) or type(document.get("protocol")) is not int or document["protocol"] != 1:
        raise LayoutError("The host does not support native window protocol 1.")
    mode = document.get("mode")
    if mode != "native":
        if mode == "capture":
            raise LayoutError("Native Windows presentation is disabled in the Windows launcher.")
        raise LayoutError("The host returned an unknown window presentation mode.")


def occlusion_limit(document: object) -> int:
    """Use clipping only when the host explicitly advertises tile coordinates."""
    parse_presentation(document)
    capabilities = document.get("capabilities", {})
    if not isinstance(capabilities, dict):
        raise LayoutError("The host returned invalid native window capabilities.")
    occlusions = capabilities.get("occlusions")
    if occlusions is None:
        return 0
    if not isinstance(occlusions, dict):
        raise LayoutError("The host returned invalid native window occlusion support.")
    maximum = occlusions.get("maxRectsPerWindow")
    coordinates = occlusions.get("coordinates")
    if not _integer(maximum) or maximum < 1 or not isinstance(coordinates, str):
        raise LayoutError("The host returned invalid native window occlusion limits.")
    return min(maximum, 16) if coordinates == "tile" else 0


def parse_layout_ack(document: object) -> str:
    """Return the host suspension reason from an acknowledged full layout."""
    if not isinstance(document, dict) or document.get("accepted") is not True:
        raise LayoutError("The host rejected the native window layout.")
    reason = document.get("suspended", "")
    if reason not in ("", "host_inactive", "display_unavailable", "aspect_mismatch"):
        raise LayoutError("The host returned an unknown native window suspension reason.")
    return reason


def parse_locked(document: object) -> bool:
    """Accept explicit lock state only; missing/unknown state must hide windows."""
    if isinstance(document, bool):
        return document
    if isinstance(document, dict) and isinstance(document.get("locked"), bool):
        return document["locked"]
    raise LayoutError("Hyprland lock state is unavailable.")


def output_state(monitors: object) -> tuple[dict, dict]:
    if not isinstance(monitors, list) or len(monitors) != 1 or not isinstance(monitors[0], dict):
        raise LayoutError("Native windows currently require exactly one Omarchy output.")
    monitor = monitors[0]
    for key in ("id", "x", "y", "width", "height", "transform"):
        if not _integer(monitor.get(key)):
            raise LayoutError(f"Hyprland returned an invalid output {key}.")
    width, height = monitor["width"], monitor["height"]
    if not (64 <= width <= MAX_DIMENSION and 64 <= height <= MAX_DIMENSION):
        raise LayoutError("The Omarchy output dimensions are unsupported.")
    scale = monitor.get("scale")
    if isinstance(scale, bool) or not isinstance(scale, (int, float)) or not math.isfinite(scale) or scale != 1:
        raise LayoutError("Native windows currently require output scale 100%.")
    if monitor["transform"] != 0:
        raise LayoutError("Native windows currently require an unrotated output.")
    workspace = monitor.get("activeWorkspace")
    if not isinstance(workspace, dict) or not _integer(workspace.get("id")) or workspace["id"] <= 0:
        raise LayoutError("The active Omarchy workspace is unknown.")
    special = monitor.get("specialWorkspace")
    if not isinstance(special, dict) or not _integer(special.get("id")):
        raise LayoutError("The Omarchy special workspace state is unknown.")
    return {"width": width, "height": height}, monitor


def compute_layout(windows: object, clients: object, monitors: object, locked: bool, pid: int,
                   *, max_occlusions: int = 0) -> dict:
    output, monitor = output_state(monitors)
    if not isinstance(locked, bool):
        raise LayoutError("The Omarchy lock state is unknown.")
    if not isinstance(windows, list) or len(windows) > MAX_WINDOWS:
        raise LayoutError("Too many native Windows windows are selected.")
    if not isinstance(clients, list):
        raise LayoutError("Hyprland returned an invalid client list.")
    if not _integer(pid) or pid <= 0:
        raise LayoutError("The native proxy process is unknown.")
    if not _integer(max_occlusions) or not 0 <= max_occlusions <= 16:
        raise LayoutError("The native window occlusion limit is unsupported.")
    ids = [item.get("id") if isinstance(item, dict) else None for item in windows]
    if len(ids) != len(set(ids)):
        raise LayoutError("The host returned duplicate native window IDs.")
    result = []
    proxies: set[int] = set()
    floating_proxies: dict[str, bool] = {}
    for ident in ids:
        marker = marker_for(ident)
        matches = [client for client in clients if isinstance(client, dict)
                   and client.get("pid") == pid and isinstance(client.get("title"), str)
                   and client["title"].endswith(marker)]
        if len(matches) > 1:
            raise LayoutError("Hyprland returned duplicate native proxy windows.")
        hidden = {"id": ident, "x": 0, "y": 0, "width": 1, "height": 1, "visible": False}
        if not matches:
            result.append(hidden)
            continue
        client = matches[0]
        proxies.add(id(client))
        if max_occlusions:
            if not isinstance(client.get("floating"), bool):
                raise LayoutError("The native proxy stacking state is unknown.")
            floating_proxies[ident] = client["floating"]
        workspace = client.get("workspace")
        if not isinstance(workspace, dict) or not _integer(workspace.get("id")):
            raise LayoutError("The native proxy workspace is unknown.")
        name = workspace.get("name")
        if not isinstance(name, str):
            raise LayoutError("The native proxy workspace name is unknown.")
        special = monitor["specialWorkspace"]
        active = monitor["activeWorkspace"]
        visible = (not locked and monitor.get("dpmsStatus") is True
                   and special["id"] == 0 and not name.startswith("special:")
                   and client.get("mapped") is True and client.get("visible") is True
                   and client.get("hidden") is False and client.get("monitor") == monitor["id"]
                   and (workspace["id"] == active["id"] or client.get("pinned") is True))
        if not visible:
            result.append(hidden)
            continue
        at, size = client.get("at"), client.get("size")
        if not (isinstance(at, list) and isinstance(size, list) and len(at) == len(size) == 2
                and all(_integer(value) for value in at + size)):
            raise LayoutError("Hyprland returned malformed native proxy coordinates.")
        x, y = at[0] - monitor["x"], at[1] - monitor["y"]
        width, height = size
        if not (0 <= x < output["width"] and 0 <= y < output["height"]
                and 0 < width <= output["width"] - x and 0 < height <= output["height"] - y):
            # An offscreen or partly cropped proxy must not steer the host window.
            result.append(hidden)
            continue
        result.append({"id": ident, "x": x, "y": y, "width": width,
                       "height": height, "visible": True})
    if max_occlusions:
        # Hyprland layers and child popups are omitted: their bounding boxes
        # do not prove opaque pixels and would hide native content incorrectly.
        for tile in result:
            if not tile["visible"]:
                continue
            rects = []
            left, top = tile["x"], tile["y"]
            right, bottom = left + tile["width"], top + tile["height"]
            for client in clients:
                if not isinstance(client, dict) or id(client) in proxies:
                    continue
                if (client.get("mapped") is False or client.get("visible") is False
                        or client.get("hidden") is True):
                    continue
                if not _integer(client.get("monitor")):
                    raise LayoutError("Hyprland returned an unknown client output.")
                if client.get("monitor") != monitor["id"]:
                    continue
                workspace = client.get("workspace")
                if not isinstance(workspace, dict) or not _integer(workspace.get("id")):
                    raise LayoutError("Hyprland returned an unknown floating window workspace.")
                if not isinstance(client.get("pinned"), bool):
                    raise LayoutError("Hyprland returned an unknown floating window pin state.")
                if workspace["id"] != monitor["activeWorkspace"]["id"] and client.get("pinned") is not True:
                    continue
                if (client.get("mapped") is not True or client.get("visible") is not True
                        or client.get("hidden") is not False or not isinstance(client.get("floating"), bool)):
                    raise LayoutError("Hyprland returned uncertain floating window visibility.")
                if client["floating"] is False:
                    continue
                at, size = client.get("at"), client.get("size")
                if not (isinstance(at, list) and isinstance(size, list) and len(at) == len(size) == 2
                        and all(_integer(value) for value in at + size)
                        and size[0] > 0 and size[1] > 0):
                    raise LayoutError("Hyprland returned malformed floating window coordinates.")
                float_left, float_top = at[0] - monitor["x"], at[1] - monitor["y"]
                x0, y0 = max(left, float_left), max(top, float_top)
                x1, y1 = min(right, float_left + size[0]), min(bottom, float_top + size[1])
                if x0 >= x1 or y0 >= y1:
                    continue
                if floating_proxies[tile["id"]]:
                    # hyprctl clients gives no trustworthy ordering between
                    # floating clients. A clipped rear window would punch a
                    # false hole into a front native proxy.
                    raise LayoutError("Floating native and Omarchy windows overlap; stacking order is unknown.")
                rects.append({"x": x0 - left, "y": y0 - top, "width": x1 - x0, "height": y1 - y0})
                if len(rects) > max_occlusions:
                    raise LayoutError("Too many floating windows overlap a native tile.")
            if rects:
                tile["occlusions"] = rects
    return {"output": output, "windows": result}
