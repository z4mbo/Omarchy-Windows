"""Recorded Hyprland client shape and native projection safety cases."""

import copy
import os
from pathlib import Path
import runpy
import subprocess
import sys
import types
import unittest
from urllib.error import HTTPError
from unittest import mock

script_dir = Path(__file__).resolve().parent
helper_dir = script_dir if (script_dir / "omarchy_windows_native_layout.py").exists() else script_dir.parent / "factory-overlay/usr/local/bin"
sys.path.insert(0, str(helper_dir))

from omarchy_windows_native_layout import LayoutError, WorkspaceMove, compute_layout, inherited_workspace, legacy_dispatch_needs_lua, marker_for, occlusion_limit, parse_layout_ack, parse_locked, parse_presentation, proxy_location, workspace_move_command
from omarchy_windows_protocol import BridgeError

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
    def test_first_layout_commits_hidden_full_state_before_visible_tile(self):
        fcntl = types.ModuleType("fcntl")
        with mock.patch.dict(sys.modules, {"fcntl": fcntl}):
            script = runpy.run_path(str(helper_dir / "omarchy-windows-native"), run_name="native_bootstrap_test")
        output = {"width": 1280, "height": 720}
        catalog = [{"id": WINDOW_ID}]
        visible = {"output": output, "windows": [
            {"id": WINDOW_ID, "x": 40, "y": 50, "width": 300, "height": 200, "visible": True}]}
        initial = script["layout_with_bootstrap"](output, catalog, visible, False)
        self.assertEqual(initial["windows"], [
            {"id": WINDOW_ID, "x": 0, "y": 0, "width": 1, "height": 1, "visible": False}])
        self.assertEqual(script["layout_with_bootstrap"](output, catalog, visible, True), visible)

    def test_layout_conflict_has_actionable_wording_without_losing_http_status(self):
        fcntl = types.ModuleType("fcntl")
        with mock.patch.dict(sys.modules, {"fcntl": fcntl}):
            script = runpy.run_path(str(helper_dir / "omarchy-windows-native"), run_name="native_test")

        def bridge_error(path, status):
            error = BridgeError(f"The Windows bridge returned HTTP {status}.")
            error.__cause__ = HTTPError("http://127.0.0.1:4445" + path, status, "test", {}, None)
            return error

        layout_error = bridge_error("/v1/layout", 409)
        self.assertTrue(script["layout_conflict"](layout_error))
        message = script["layout_error_message"](layout_error)
        self.assertIn("Reopen Windows Apps in Omarchy", message)
        self.assertIn("Stop showing Windows app", message)
        self.assertNotIn("HTTP 409", message)
        self.assertEqual(layout_error.__cause__.code, 409)
        for error in (bridge_error("/v1/windows", 409), bridge_error("/v1/layout", 500),
                      LayoutError("Hyprland is unavailable")):
            self.assertFalse(script["layout_conflict"](error))
            self.assertEqual(script["layout_error_message"](error), str(error))

    def test_gtk_proxy_waits_for_verified_workspace_before_fullscreen(self):
        controllers = []
        removed_sources = []

        class FakeApplication:
            def __init__(self, **_kwargs):
                pass

            def run(self, _args):
                controllers.append(self)
                return 0

        class FakeWindow:
            def __init__(self, **_kwargs):
                self.fullscreen_calls = 0
                self.unfullscreen_calls = 0
                self.present_calls = 0
                self.visible = False

            def set_title(self, _title):
                pass

            def set_default_size(self, _width, _height):
                pass

            def set_child(self, _child):
                pass

            def connect(self, *_args):
                return 1

            def disconnect(self, _handler):
                pass

            def set_visible(self, value):
                self.visible = value

            def present(self):
                self.present_calls += 1

            def fullscreen(self):
                self.fullscreen_calls += 1

            def unfullscreen(self):
                self.unfullscreen_calls += 1

            def close(self):
                pass

        class FakeLabel:
            def __init__(self, **_kwargs):
                pass

            def set_wrap(self, _value):
                pass

        gi = types.ModuleType("gi")
        gi.require_version = lambda *_args: None
        repository = types.ModuleType("gi.repository")
        repository.Gio = types.SimpleNamespace(ApplicationFlags=types.SimpleNamespace(NON_UNIQUE=1))
        repository.GLib = types.SimpleNamespace(timeout_add=lambda _delay, _callback: 73,
                                                source_remove=removed_sources.append)
        repository.Gtk = types.SimpleNamespace(Application=FakeApplication,
                                               ApplicationWindow=FakeWindow, Label=FakeLabel)
        gi.repository = repository
        fcntl = types.ModuleType("fcntl")
        with mock.patch.dict(sys.modules, {"gi": gi, "gi.repository": repository, "fcntl": fcntl}):
            script = runpy.run_path(str(helper_dir / "omarchy-windows-native"), run_name="native_test")
            fd = os.open(os.devnull, os.O_RDONLY)
            self.assertEqual(script["start_gtk"](object(), fd, 0), 0)
            controller = controllers[0]
            controller.workspace_history["league"] = 3
            game_id = "b" * 32
            game = {"id": game_id, "title": "League game", "process": "League.exe",
                    "appGroup": "league", "width": 1280, "height": 720, "fullscreen": True}
            controller.apply_catalog([game], [])
            view = controller.views[game_id]
            self.assertTrue(view.window.visible)
            self.assertEqual(view.window.present_calls, 0)
            self.assertEqual(view.window.fullscreen_calls, 0)
            # A repeated catalog update must not bypass the pending move.
            view.update(game)
            self.assertEqual(view.window.fullscreen_calls, 0)
            location = {"pid": os.getpid(), "title": "League game" + marker_for(game_id),
                        "address": "0x1234", "mapped": True, "workspace": {"id": 1, "name": "1"}}
            view.move_to_inherited_workspace.__func__.__globals__["hyprctl_json"] = lambda _command: [location]
            with mock.patch.object(script["subprocess"], "run",
                                   return_value=subprocess.CompletedProcess([], 0, "ok", "")) as dispatch:
                self.assertTrue(view.move_to_inherited_workspace())
                self.assertEqual(dispatch.call_args.args[0], [
                    "hyprctl", "dispatch", "movetoworkspacesilent", "3,address:0x1234"])
            self.assertEqual(view.window.fullscreen_calls, 0)
            location["workspace"] = {"id": 3, "name": "3"}
            self.assertFalse(view.move_to_inherited_workspace())
            self.assertEqual(view.window.fullscreen_calls, 1)

            # If placement times out, later metadata updates must not fullscreen
            # on the wrong workspace. Retiring another pending view removes its timer.
            failed_id = "c" * 32
            failed = {**game, "id": failed_id}
            controller.apply_catalog([game, failed], [])
            failed_view = controller.views[failed_id]
            failed_view.move_to_inherited_workspace.__func__.__globals__["hyprctl_json"] = lambda _command: []
            for _ in range(12):
                failed_view.move_to_inherited_workspace()
            failed_view.update(failed)
            self.assertEqual(failed_view.window.fullscreen_calls, 0)
            failed_view.update({**failed, "fullscreen": False})
            self.assertTrue(failed_view.retry_on_fullscreen)
            failed_view.update(failed)
            self.assertIsNotNone(failed_view.move.target)
            self.assertEqual(failed_view.window.fullscreen_calls, 0)
            recovered = {**location, "title": "League game" + marker_for(failed_id)}
            failed_view.move_to_inherited_workspace.__func__.__globals__["hyprctl_json"] = lambda _command: [recovered]
            self.assertFalse(failed_view.move_to_inherited_workspace())
            self.assertEqual(failed_view.window.fullscreen_calls, 1)
            retiring_id = "d" * 32
            controller.apply_catalog([game, failed, {**game, "id": retiring_id}], [])
            controller.views[retiring_id].retire()
            self.assertIn(73, removed_sources)

            # An already-fullscreen host app can appear in the first catalog
            # before its GTK proxy is mapped. Defer the one request until
            # Hyprland reports that exact proxy as a mapped client.
            standalone_id = "e" * 32
            standalone = {**game, "id": standalone_id, "title": "Blender",
                          "appGroup": "blender"}
            controller.apply_catalog([standalone], [])
            standalone_view = controller.views[standalone_id]
            self.assertEqual(standalone_view.window.fullscreen_calls, 0)
            mapped = {"pid": os.getpid(), "title": "Blender" + marker_for(standalone_id),
                      "address": "0x5678", "mapped": True,
                      "workspace": {"id": 2, "name": "2"}}
            controller.apply_catalog([standalone], [mapped])
            self.assertEqual(standalone_view.window.fullscreen_calls, 1)
            controller.apply_catalog([standalone], [mapped])
            self.assertEqual(standalone_view.window.fullscreen_calls, 1)
            controller.apply_catalog([standalone], [])
            controller.apply_catalog([standalone], [mapped])
            self.assertEqual(standalone_view.window.fullscreen_calls, 2)
            controller.apply_catalog([{**standalone, "fullscreen": False}], [mapped])
            self.assertEqual(standalone_view.window.unfullscreen_calls, 1)
            controller.apply_catalog([standalone], [mapped])
            self.assertEqual(standalone_view.window.fullscreen_calls, 3)

    def test_new_fullscreen_game_inherits_client_workspace(self):
        _, clients, _ = state()
        clients[0]["address"] = "0x563190857090"
        clients[0]["workspace"] = {"id": 4, "name": "4"}
        known = {WINDOW_ID: "league"}
        self.assertEqual(proxy_location(WINDOW_ID, clients, PID), (4, "0x563190857090"))
        self.assertEqual(inherited_workspace("league", known, clients, PID, {}), 4)
        self.assertIsNone(inherited_workspace("explorer", known, clients, PID, {}))

    def test_hyprland_move_selects_lua_only_on_specific_parser_hint(self):
        self.assertEqual(workspace_move_command(3, "0x1234", "lua"), [
            "hyprctl", "dispatch",
            'hl.dsp.window.move({ workspace = 3, follow = false, window = "address:0x1234" })'])
        self.assertEqual(workspace_move_command(3, "0x1234", "legacy"), [
            "hyprctl", "dispatch", "movetoworkspacesilent", "3,address:0x1234"])
        for workspace, address in ((True, "0x1234"), (0, "0x1234"),
                                   (3, '0x1234" }) ; os.execute("bad")')):
            with self.assertRaises(LayoutError):
                workspace_move_command(workspace, address, "lua")
        hint = "Note: dispatch in lua is a shorthand for hl.dispatch(...), your syntax might need to be updated."
        self.assertTrue(legacy_dispatch_needs_lua(hint))
        self.assertFalse(legacy_dispatch_needs_lua("invalid window address"))
        fcntl = types.ModuleType("fcntl")
        with mock.patch.dict(sys.modules, {"fcntl": fcntl}):
            script = runpy.run_path(str(helper_dir / "omarchy-windows-native"), run_name="native_test")
        failed = subprocess.CompletedProcess([], 7, "", hint)
        ok = subprocess.CompletedProcess([], 0, "ok", "")
        with mock.patch.object(script["subprocess"], "run", side_effect=[failed, ok, ok]) as dispatch:
            syntax = script["dispatch_workspace_move"](3, "0x1234", None)
            self.assertEqual(syntax, "lua")
            self.assertEqual(dispatch.call_args_list[0].args[0],
                             workspace_move_command(3, "0x1234", "legacy"))
            self.assertEqual(dispatch.call_args_list[1].args[0],
                             workspace_move_command(3, "0x1234", "lua"))
            self.assertEqual(script["dispatch_workspace_move"](3, "0x1234", syntax), "lua")
            self.assertEqual(dispatch.call_count, 3)
        with mock.patch.object(script["subprocess"], "run",
                               return_value=subprocess.CompletedProcess([], 7, "", "invalid window address")) as dispatch:
            self.assertIsNone(script["dispatch_workspace_move"](3, "0x1234", None))
            dispatch.assert_called_once()

    def test_gone_client_uses_history_but_ambiguous_proxy_is_rejected(self):
        _, clients, _ = state()
        clients[0]["address"] = "0x563190857090"
        history = {"league": 3}
        known = {WINDOW_ID: "league"}
        self.assertEqual(inherited_workspace("league", known, [], PID, history), 3)
        self.assertIsNone(inherited_workspace("league", known, clients, PID + 1, {}))
        self.assertIsNone(inherited_workspace("league", known, clients * 2, PID, history))
        clients[0]["mapped"] = False
        self.assertIsNone(proxy_location(WINDOW_ID, clients, PID))
        self.assertIsNone(inherited_workspace("league", known, clients, PID, history))

    def test_workspace_move_blocks_fullscreen_until_verified_and_cancels_timer(self):
        move = WorkspaceMove(3)
        move.source_id = 72
        self.assertFalse(move.can_fullscreen())
        self.assertEqual([move.retry() for _ in range(12)], [True] * 11 + [False])
        move.fail()
        self.assertFalse(move.can_fullscreen())
        self.assertIsNone(move.target)
        retired = WorkspaceMove(3)
        retired.source_id = 73
        removed = []
        retired.cancel(removed.append)
        self.assertEqual(removed, [73])
        self.assertIsNone(retired.target)
        verified = WorkspaceMove(3)
        verified.complete()
        self.assertTrue(verified.can_fullscreen())

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
