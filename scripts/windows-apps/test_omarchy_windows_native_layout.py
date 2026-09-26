"""Recorded Hyprland client shape and native projection safety cases."""

import copy
from pathlib import Path
import sys
import unittest

script_dir = Path(__file__).resolve().parent
helper_dir = script_dir if (script_dir / "omarchy_windows_native_layout.py").exists() else script_dir.parent / "factory-overlay/usr/local/bin"
sys.path.insert(0, str(helper_dir))

from omarchy_windows_native_layout import LayoutError, compute_layout, marker_for, occlusion_limit, parse_layout_ack, parse_locked, parse_presentation

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
        "pid": PID, "pinned": False, "floating": False,
        "title": "Character Map" + marker_for(WINDOW_ID),
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

    def test_occlusion_capability_is_optional_and_tile_relative_only(self):
        self.assertEqual(occlusion_limit({"mode": "native", "protocol": 1}), 0)
        self.assertEqual(occlusion_limit({"mode": "native", "protocol": 1, "capabilities": {
            "occlusions": {"maxRectsPerWindow": 16, "coordinates": "tile"}}}), 16)
        self.assertEqual(occlusion_limit({"mode": "native", "protocol": 1, "capabilities": {
            "occlusions": {"maxRectsPerWindow": 32, "coordinates": "tile"}}}), 16)
        self.assertEqual(occlusion_limit({"mode": "native", "protocol": 1, "capabilities": {
            "occlusions": {"maxRectsPerWindow": 16, "coordinates": "output"}}}), 0)
        for capabilities in ([], {"occlusions": []},
                             {"occlusions": {"maxRectsPerWindow": True, "coordinates": "tile"}},
                             {"occlusions": {"maxRectsPerWindow": 0, "coordinates": "tile"}}):
            with self.subTest(capabilities=capabilities), self.assertRaises(LayoutError):
                occlusion_limit({"mode": "native", "protocol": 1, "capabilities": capabilities})

    def test_floating_overlap_clips_only_with_advertised_capability(self):
        windows, clients, monitors = state()
        # The proxy is at 490,260 with size 300x226. A Linux floating window
        # overlaps its right edge, and the occlusion uses proxy-local pixels.
        clients.append({"pid": PID + 1, "mapped": True, "visible": True, "hidden": False,
                        "floating": True, "pinned": False, "monitor": 0,
                        "workspace": {"id": 1, "name": "1"}, "at": [700, 300], "size": [150, 100]})
        ordinary = compute_layout(windows, clients, monitors, False, PID)
        self.assertNotIn("occlusions", ordinary["windows"][0])
        clipped = compute_layout(windows, clients, monitors, False, PID, max_occlusions=16)
        self.assertEqual(clipped["windows"][0]["occlusions"], [
            {"x": 210, "y": 40, "width": 90, "height": 100}])

    def test_hidden_off_workspace_and_proxy_clients_do_not_clip(self):
        windows, clients, monitors = state()
        proxy = clients[0]
        proxy["floating"] = True
        off_workspace = {"pid": PID + 1, "mapped": True, "visible": True,
                         "hidden": False, "floating": True, "pinned": False,
                         "monitor": 0, "workspace": {"id": 2, "name": "2"},
                         "at": [500, 270], "size": [100, 100]}
        hidden = {**off_workspace, "workspace": {"id": 1, "name": "1"}, "hidden": True}
        clients += [off_workspace, hidden]
        tile = compute_layout(windows, clients, monitors, False, PID, max_occlusions=16)["windows"][0]
        self.assertNotIn("occlusions", tile)

    def test_many_or_malformed_visible_float_clients_fail_closed(self):
        windows, clients, monitors = state()
        floating = {"pid": PID + 1, "mapped": True, "visible": True,
                    "hidden": False, "floating": True, "pinned": False,
                    "monitor": 0, "workspace": {"id": 1, "name": "1"},
                    "at": [500, 270], "size": [100, 100]}
        clients += [{**floating} for _ in range(17)]
        with self.assertRaises(LayoutError):
            compute_layout(windows, clients, monitors, False, PID, max_occlusions=16)
        clients = state()[1] + [{**floating, "size": [100, False]}]
        with self.assertRaises(LayoutError):
            compute_layout(windows, clients, monitors, False, PID, max_occlusions=16)

    def test_overlapping_floating_proxy_rejects_unknown_stacking_order(self):
        windows, clients, monitors = state()
        clients[0]["floating"] = True
        clients.append({"pid": PID + 1, "mapped": True, "visible": True,
                        "hidden": False, "floating": True, "pinned": False,
                        "monitor": 0, "workspace": {"id": 1, "name": "1"},
                        "at": [500, 270], "size": [100, 100]})
        with self.assertRaisesRegex(LayoutError, "stacking order"):
            compute_layout(windows, clients, monitors, False, PID, max_occlusions=16)

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
