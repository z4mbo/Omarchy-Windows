"""Recorded Hyprland client shape and native projection safety cases."""

import copy
from pathlib import Path
import sys
import unittest

script_dir = Path(__file__).resolve().parent
helper_dir = script_dir if (script_dir / "omarchy_windows_native_layout.py").exists() else script_dir.parent / "factory-overlay/usr/local/bin"
sys.path.insert(0, str(helper_dir))

from omarchy_windows_native_layout import LayoutError, compute_layout, marker_for, parse_layout_ack, parse_locked, parse_presentation

WINDOW_ID = "a" * 32
PID = 4607


def state():
    windows = [{"id": WINDOW_ID}]
    monitors = [{
        "id": 0, "name": "Virtual-1", "x": 0, "y": 0, "width": 1280,
        "height": 720, "scale": 1.0, "transform": 0, "dpmsStatus": True,
        "activeWorkspace": {"id": 1, "name": "1"},
        "specialWorkspace": {"id": 0, "name": ""},
    }]
    # Client fields are from the disposable guest's recorded hyprctl output.
    clients = [{
        "address": "0x563190857090", "mapped": True, "hidden": False,
        "visible": True, "at": [490, 260], "size": [300, 226],
        "workspace": {"id": 1, "name": "1"}, "monitor": 0,
        "pid": PID, "pinned": False, "title": "Character Map" + marker_for(WINDOW_ID),
    }]
    return windows, clients, monitors


class NativeLayoutTest(unittest.TestCase):
    def test_mapped_client_uses_output_relative_rect(self):
        windows, clients, monitors = state()
        monitors[0]["x"] = 100
        monitors[0]["y"] = 50
        layout = compute_layout(windows, clients, monitors, False, PID)
        self.assertEqual(layout, {"output": {"width": 1280, "height": 720}, "windows": [
            {"id": WINDOW_ID, "x": 390, "y": 210, "width": 300, "height": 226, "visible": True}]})

    def test_workspace_lock_sleep_special_and_offscreen_hide(self):
        for variant in ("workspace", "lock", "sleep", "special", "offscreen", "missing", "wrong_pid"):
            with self.subTest(variant=variant):
                windows, clients, monitors = state()
                locked = False
                if variant == "workspace":
                    monitors[0]["activeWorkspace"]["id"] = 2
                elif variant == "lock":
                    locked = True
                elif variant == "sleep":
                    monitors[0]["dpmsStatus"] = False
                elif variant == "special":
                    monitors[0]["specialWorkspace"]["id"] = -99
                elif variant == "offscreen":
                    clients[0]["at"] = [1200, 260]
                elif variant == "missing":
                    clients = []
                else:
                    clients[0]["pid"] = PID + 1
                layout = compute_layout(windows, clients, monitors, locked, PID)
                self.assertEqual(layout["windows"], [
                    {"id": WINDOW_ID, "x": 0, "y": 0, "width": 1, "height": 1, "visible": False}])

    def test_ambiguous_or_unsupported_state_fails_closed(self):
        for mutation in ("two_outputs", "scale", "transform", "missing_special", "bad_client_geometry", "duplicate"):
            with self.subTest(mutation=mutation):
                windows, clients, monitors = state()
                if mutation == "two_outputs":
                    monitors.append(copy.deepcopy(monitors[0]))
                elif mutation == "scale":
                    monitors[0]["scale"] = 1.25
                elif mutation == "transform":
                    monitors[0]["transform"] = 1
                elif mutation == "missing_special":
                    del monitors[0]["specialWorkspace"]
                elif mutation == "bad_client_geometry":
                    clients[0]["size"] = [False, 200]
                else:
                    clients.append(copy.deepcopy(clients[0]))
                with self.assertRaises(LayoutError):
                    compute_layout(windows, clients, monitors, False, PID)

    def test_mode_and_lock_require_explicit_values(self):
        parse_presentation({"mode": "native", "protocol": 1})
        self.assertFalse(parse_locked({"locked": False}))
        self.assertTrue(parse_locked(True))
        for document in ({"mode": "capture", "protocol": 1}, {"mode": "native", "protocol": 2}):
            with self.assertRaises(LayoutError):
                parse_presentation(document)
        for document in (None, {}, {"locked": "false"}):
            with self.assertRaises(LayoutError):
                parse_locked(document)

    def test_layout_ack_distinguishes_focus_suspension_from_display_failure(self):
        self.assertEqual(parse_layout_ack({"accepted": True}), "")
        self.assertEqual(parse_layout_ack({"accepted": True, "suspended": "host_inactive"}), "host_inactive")
        self.assertEqual(parse_layout_ack({"accepted": True, "suspended": "aspect_mismatch"}), "aspect_mismatch")
        self.assertEqual(parse_layout_ack({"accepted": True, "suspended": "display_unavailable"}), "display_unavailable")
        for document in (None, {}, {"accepted": False}, {"accepted": 1},
                         {"accepted": True, "suspended": "other"}):
            with self.assertRaises(LayoutError):
                parse_layout_ack(document)


if __name__ == "__main__":
    unittest.main()
