"""Coordinate and event handling shared by the GTK presenter and its tests."""

from __future__ import annotations

from collections import deque
import threading
from typing import Callable

from omarchy_windows_protocol import BridgeError

MAX_PENDING_INPUT = 256


class InputMailboxClosed(RuntimeError):
    """Input stopped because the host bridge could not drain its backlog."""


def image_point(x: float, y: float, allocation: tuple[int, int], frame: tuple[int, int]) -> tuple[int, int]:
    """Map a Gtk.Picture FILL coordinate to the captured window's pixels."""
    width, height = allocation
    frame_width, frame_height = frame
    if min(width, height, frame_width, frame_height) <= 0:
        raise ValueError("Picture and frame dimensions must be positive")
    return (
        min(frame_width - 1, max(0, int(x * frame_width / width))),
        min(frame_height - 1, max(0, int(y * frame_height / height))),
    )


def frame_matches_window(frame: tuple[int, int], metadata: dict) -> bool:
    """A captured PNG can receive clicks only for its current HWND bounds."""
    return frame == (metadata["width"], metadata["height"])


def should_forward_key(down: bool, frame_ready: bool, was_forwarded: bool) -> bool:
    """Block new keys on stale frames but finish keys already sent to Windows."""
    return frame_ready if down else was_forwarded


class InputMailbox:
    """Keep every button/key edge while collapsing unneeded pointer motion.

    Motion may replace only an adjacent motion for the same window. That
    preserves press, drag, release, and cross-window ordering.
    Network input has its own worker, so a slow PNG capture cannot block it.
    """

    def __init__(self) -> None:
        self._events: deque[tuple[str, str, dict | None]] = deque()
        self._changed = threading.Condition()
        self._closed = False

    def put(self, command: str, ident: str, payload: dict | None = None) -> None:
        item = (command, ident, payload)
        motion = command == "input" and payload is not None and payload.get("type") == "pointer" and not payload.get("button")
        with self._changed:
            if self._closed:
                raise InputMailboxClosed("Windows app input has stopped after a bridge backlog.")
            if motion and self._events:
                previous = self._events[-1]
                if previous[0] == "input" and previous[1] == ident and previous[2] is not None and previous[2].get("type") == "pointer" and not previous[2].get("button"):
                    self._events[-1] = item
                    return
            if len(self._events) >= MAX_PENDING_INPUT:
                self._closed = True
                self._events.clear()
                self._changed.notify_all()
                raise InputMailboxClosed("Windows app input stopped because the bridge did not keep up.")
            self._events.append(item)
            self._changed.notify()

    def take(self, timeout: float) -> tuple[str, str, dict | None] | None:
        with self._changed:
            if not self._events:
                self._changed.wait(timeout)
            return self._events.popleft() if self._events else None

    def close(self) -> None:
        with self._changed:
            self._closed = True
            self._events.clear()
            self._changed.notify_all()


def run_input_worker(mailbox: InputMailbox, bridge: object, stop: threading.Event,
                     on_error: Callable[[BridgeError], None] | None = None) -> None:
    """Send input independently of the frame polling/capture thread."""
    while not stop.is_set():
        item = mailbox.take(0.1)
        if item is None:
            continue
        command, ident, payload = item
        try:
            if command == "close":
                bridge.close(ident)
            elif payload is not None:
                bridge.input(ident, payload)
        except BridgeError as exc:
            # A POST timeout is ambiguous. Never replay a press or key event:
            # fail this presenter and require a fresh user action instead.
            mailbox.close()
            stop.set()
            if on_error is not None:
                on_error(exc)
            return
