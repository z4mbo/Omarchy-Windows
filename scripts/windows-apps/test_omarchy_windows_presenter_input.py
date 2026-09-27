"""Presenter coordinate and input ordering checks without a GTK session."""

from pathlib import Path
import sys
import threading
import unittest

script_dir = Path(__file__).resolve().parent
module_dir = script_dir if (script_dir / "omarchy_windows_presenter_input.py").exists() else script_dir.parent / "factory-overlay/usr/local/bin"
sys.path.insert(0, str(module_dir))

from omarchy_windows_presenter_input import InputMailbox, InputMailboxClosed, MAX_PENDING_INPUT, frame_matches_window, image_point, run_input_worker, should_forward_key
from omarchy_windows_protocol import BridgeError


class PresenterInputTests(unittest.TestCase):
    def test_character_map_coordinates_at_tiled_and_fullscreen_sizes(self):
        # Physical Character Map capture was aligned to its 491x437 HWND.
        # The B-cell click sent through the isolated bridge was (316, 117).
        frame = (491, 437)
        self.assertEqual(image_point(316, 117, frame, frame), (316, 117))
        self.assertEqual(image_point(158.4, 58.7, (246, 219), frame), (316, 117))
        self.assertEqual(image_point(1264, 468, (1964, 1748), frame), (316, 117))
        self.assertEqual(image_point(9999, -2, (1964, 1748), frame), (490, 0))

    def test_invalid_allocation_is_rejected(self):
        with self.assertRaises(ValueError):
            image_point(1, 1, (0, 400), (491, 437))

    def test_fullscreen_resize_requires_a_new_matching_frame(self):
        tiled = {"width": 491, "height": 437, "fullscreen": False}
        fullscreen = {"width": 2560, "height": 1440, "fullscreen": True}
        self.assertTrue(frame_matches_window((491, 437), tiled))
        self.assertFalse(frame_matches_window((491, 437), fullscreen))
        self.assertTrue(frame_matches_window((2560, 1440), fullscreen))

    def test_stale_frame_blocks_new_keys_but_allows_sent_key_release(self):
        self.assertFalse(should_forward_key(True, False, False))
        self.assertFalse(should_forward_key(True, False, True))  # no repeat on stale frame
        self.assertTrue(should_forward_key(False, False, True))
        self.assertFalse(should_forward_key(False, False, False))
        self.assertFalse(should_forward_key(False, True, False))
        self.assertTrue(should_forward_key(True, True, False))

    def test_motion_is_coalesced_without_losing_button_edges(self):
        mailbox = InputMailbox()
        for x in range(500):
            mailbox.put("input", "charmap", {"type": "pointer", "x": x, "y": 117})
        mailbox.put("input", "charmap", {"type": "pointer", "x": 316, "y": 117, "button": 1, "down": True})
        for x in range(500, 1000):
            mailbox.put("input", "charmap", {"type": "pointer", "x": x, "y": 117})
        mailbox.put("input", "charmap", {"type": "pointer", "x": 316, "y": 117, "button": 1, "down": False})
        self.assertEqual(
            [mailbox.take(0) for _ in range(4)],
            [
                ("input", "charmap", {"type": "pointer", "x": 499, "y": 117}),
                ("input", "charmap", {"type": "pointer", "x": 316, "y": 117, "button": 1, "down": True}),
                ("input", "charmap", {"type": "pointer", "x": 999, "y": 117}),
                ("input", "charmap", {"type": "pointer", "x": 316, "y": 117, "button": 1, "down": False}),
            ],
        )
        self.assertIsNone(mailbox.take(0))

    def test_interleaved_windows_do_not_reorder_key_or_close(self):
        mailbox = InputMailbox()
        mailbox.put("input", "one", {"type": "pointer", "x": 1})
        mailbox.put("input", "two", {"type": "pointer", "x": 2})
        mailbox.put("input", "one", {"type": "pointer", "x": 3})
        mailbox.put("input", "one", {"type": "key", "vk": 66, "down": True})
        mailbox.put("close", "two")
        self.assertEqual(mailbox.take(0), ("input", "one", {"type": "pointer", "x": 1}))
        self.assertEqual(mailbox.take(0), ("input", "two", {"type": "pointer", "x": 2}))
        self.assertEqual(mailbox.take(0), ("input", "one", {"type": "pointer", "x": 3}))
        self.assertEqual(mailbox.take(0), ("input", "one", {"type": "key", "vk": 66, "down": True}))
        self.assertEqual(mailbox.take(0), ("close", "two", None))

    def test_stalled_input_fails_closed_at_a_fixed_backlog(self):
        mailbox = InputMailbox()
        for index in range(MAX_PENDING_INPUT):
            mailbox.put("input", "charmap", {"type": "key", "vk": 65, "down": index % 2 == 0})
        with self.assertRaisesRegex(InputMailboxClosed, "did not keep up"):
            mailbox.put("input", "charmap", {"type": "key", "vk": 66, "down": True})
        self.assertIsNone(mailbox.take(0), "failed mailbox must not replay stale keys")
        with self.assertRaises(InputMailboxClosed):
            mailbox.put("input", "charmap", {"type": "pointer", "x": 316, "y": 117, "button": 1, "down": True})

    def test_input_dispatch_does_not_wait_for_a_stalled_frame(self):
        frame_started = threading.Event()
        frame_release = threading.Event()
        input_seen = threading.Event()
        stop = threading.Event()

        class Bridge:
            def frame(self):
                frame_started.set()
                frame_release.wait(2)

            def input(self, _ident, _payload):
                input_seen.set()

            def close(self, _ident):
                pass

        bridge = Bridge()
        mailbox = InputMailbox()
        capture = threading.Thread(target=bridge.frame)
        sender = threading.Thread(target=run_input_worker, args=(mailbox, bridge, stop))
        capture.start()
        sender.start()
        try:
            self.assertTrue(frame_started.wait(1))
            mailbox.put("input", "charmap", {"type": "pointer", "x": 316, "y": 117, "button": 1, "down": True})
            self.assertTrue(input_seen.wait(1), "click waited behind PNG capture")
        finally:
            stop.set()
            frame_release.set()
            capture.join(2)
            sender.join(2)

    def test_ambiguous_transport_error_closes_input_without_replay(self):
        mailbox = InputMailbox()
        stop = threading.Event()
        errors = []

        class Bridge:
            calls = 0

            def input(self, _ident, _payload):
                self.calls += 1
                raise BridgeError("timed out after POST")

            def close(self, _ident):
                raise AssertionError("queued close must not replay")

        bridge = Bridge()
        mailbox.put("input", "charmap", {"type": "pointer", "x": 316, "y": 117, "button": 1, "down": True})
        mailbox.put("close", "charmap")
        run_input_worker(mailbox, bridge, stop, errors.append)
        self.assertEqual(bridge.calls, 1)
        self.assertTrue(stop.is_set())
        self.assertEqual(len(errors), 1)
        self.assertIsNone(mailbox.take(0))
        with self.assertRaises(InputMailboxClosed):
            mailbox.put("input", "charmap", {"type": "key", "vk": 66, "down": True})


if __name__ == "__main__":
    unittest.main()
