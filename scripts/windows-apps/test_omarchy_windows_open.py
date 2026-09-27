"""The Omarchy menu routes named Windows apps through the window presenter."""

from __future__ import annotations

import importlib.machinery
import importlib.util
import os
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest import mock


SCRIPT = Path(__file__).with_name("omarchy-windows-open")
loader = importlib.machinery.SourceFileLoader("omarchy_windows_open_test_target", str(SCRIPT))
spec = importlib.util.spec_from_loader(loader.name, loader)
assert spec is not None
module = importlib.util.module_from_spec(spec)
sys.path.insert(0, str(SCRIPT.parent))
try:
    loader.exec_module(module)
finally:
    sys.path.pop(0)


class OpenTargetTests(unittest.TestCase):
    def test_named_target_matches_only_its_host_app_group_or_process(self) -> None:
        charmap = {"appGroup": "charmap", "process": "charmap.exe"}
        explorer = {"appGroup": "explorer", "process": "explorer.exe"}
        league = {"appGroup": "unknown", "process": r"C:\Games\LeagueClientUx.exe"}
        self.assertFalse(module.matches_named_target(charmap, "explorer"))
        self.assertFalse(module.matches_named_target(charmap, "league"))
        self.assertTrue(module.matches_named_target(explorer, "explorer"))
        self.assertFalse(module.matches_named_target(explorer, "league"))
        self.assertTrue(module.matches_named_target(league, "league"))

    def test_only_named_app_targets_or_picker_are_accepted(self) -> None:
        self.assertEqual(module.launch_target([]), "pick")
        for target in ("explorer", "league"):
            self.assertEqual(module.launch_target([target]), target)
        for arguments in (["arbitrary"], ["explorer", "league"], ["open", "shortcut-abc"]):
            with self.subTest(arguments=arguments), self.assertRaises(ValueError):
                module.launch_target(arguments)

    def test_invalid_argument_exits_before_any_bridge_or_launch(self) -> None:
        with mock.patch.object(module, "Bridge") as bridge, mock.patch.object(module.subprocess, "run") as run:
            self.assertEqual(module.main(["unknown"]), 2)
            bridge.assert_not_called()
            run.assert_not_called()

    def test_named_app_launch_failure_never_starts_presenter(self) -> None:
        class Bridge:
            def __init__(self, _token: str) -> None:
                pass

            def presentation_mode(self) -> str:
                return "native"

            def windows(self) -> list[dict]:
                return []

        for target in ("explorer", "league", "pick"):
            with self.subTest(target=target):
                calls: list[tuple[list[str], dict]] = []
                messages: list[str] = []

                def run(arguments: list[str], **kwargs: object) -> SimpleNamespace:
                    calls.append((arguments, kwargs))
                    return SimpleNamespace(returncode=0 if arguments[-1] == "--probe" else 1)

                with (mock.patch.dict(os.environ, {"WAYLAND_DISPLAY": "wayland-test"}),
                      mock.patch.object(module, "Bridge", Bridge),
                      mock.patch.object(module, "load_token", return_value="test-token"),
                      mock.patch.object(module, "message", side_effect=messages.append),
                      mock.patch.object(module.subprocess, "run", side_effect=run),
                      mock.patch.object(module.subprocess, "Popen") as popen):
                    self.assertEqual(module.main([] if target == "pick" else [target]), 1)
                    popen.assert_not_called()
                self.assertEqual(len(calls), 2)
                self.assertEqual(calls[0][0][-1], "--probe")
                self.assertEqual(calls[1][0][-1], target)
                self.assertEqual(calls[1][1]["env"]["OMARCHY_WINDOW_PREVIEW"], "1")
                self.assertTrue(any("could not open" in item.lower() for item in messages))

    def test_named_explorer_ignores_unrelated_grants_while_waiting_for_selector(self) -> None:
        unrelated = {"id": "character-map", "appGroup": "charmap", "process": "charmap.exe"}
        new_unrelated = {"id": "calculator", "appGroup": "calc", "process": "Calculator.exe"}

        class Bridge:
            calls = 0

            def __init__(self, _token: str) -> None:
                pass

            def presentation_mode(self) -> str:
                return "native"

            def windows(self) -> list[dict]:
                self.calls += 1
                return [unrelated] if self.calls == 1 else [unrelated, new_unrelated]

        messages: list[str] = []
        with (mock.patch.dict(os.environ, {"WAYLAND_DISPLAY": "wayland-test"}),
              mock.patch.object(module, "Bridge", Bridge),
              mock.patch.object(module, "load_token", return_value="test-token"),
              mock.patch.object(module, "message", side_effect=messages.append),
              mock.patch.object(module.subprocess, "run", return_value=SimpleNamespace(returncode=0)),
              mock.patch.object(module.subprocess, "Popen") as popen,
              mock.patch.object(module.time, "monotonic", side_effect=[0, 0, 1, 12]),
              mock.patch.object(module.time, "sleep")):
            self.assertEqual(module.main(["explorer"]), 1)
            popen.assert_not_called()
        self.assertTrue(any("no matching window" in text and "tray" in text for text in messages))

    def test_named_explorer_uses_new_matching_grant_after_unrelated_one(self) -> None:
        unrelated = {"id": "character-map", "appGroup": "charmap", "process": "charmap.exe"}
        explorer = {"id": "explorer-new", "appGroup": "explorer", "process": "explorer.exe"}

        class Bridge:
            calls = 0

            def __init__(self, _token: str) -> None:
                pass

            def presentation_mode(self) -> str:
                return "native"

            def windows(self) -> list[dict]:
                self.calls += 1
                return [unrelated] if self.calls <= 2 else [unrelated, explorer]

        with (mock.patch.dict(os.environ, {"WAYLAND_DISPLAY": "wayland-test"}),
              mock.patch.object(module, "Bridge", Bridge),
              mock.patch.object(module, "load_token", return_value="test-token"),
              mock.patch.object(module.subprocess, "run", return_value=SimpleNamespace(returncode=0)),
              mock.patch.object(module.subprocess, "Popen") as popen,
              mock.patch.object(module.time, "monotonic", side_effect=[0, 0, 1]),
              mock.patch.object(module.time, "sleep")):
            self.assertEqual(module.main(["explorer"]), 0)
            popen.assert_called_once()

    def test_named_explorer_can_reuse_only_an_existing_explorer_grant(self) -> None:
        unrelated = {"id": "character-map", "appGroup": "charmap", "process": "charmap.exe"}
        explorer = {"id": "explorer-existing", "appGroup": "explorer", "process": "explorer.exe"}

        class Bridge:
            def __init__(self, _token: str) -> None:
                pass

            def presentation_mode(self) -> str:
                return "native"

            def windows(self) -> list[dict]:
                return [unrelated, explorer]

        with (mock.patch.dict(os.environ, {"WAYLAND_DISPLAY": "wayland-test"}),
              mock.patch.object(module, "Bridge", Bridge),
              mock.patch.object(module, "load_token", return_value="test-token"),
              mock.patch.object(module.subprocess, "run", return_value=SimpleNamespace(returncode=0)),
              mock.patch.object(module.subprocess, "Popen") as popen,
              mock.patch.object(module.time, "monotonic", side_effect=[0, 0, 12]),
              mock.patch.object(module.time, "sleep")):
            self.assertEqual(module.main(["explorer"]), 0)
            popen.assert_called_once()


if __name__ == "__main__":
    unittest.main()
