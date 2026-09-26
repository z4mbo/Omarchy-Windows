"""The guest never trusts unbounded host responses or sends its token to a proxy."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import struct
import sys
import tempfile
import threading
import unittest

script_dir = Path(__file__).resolve().parent
protocol_dir = script_dir if (script_dir / "omarchy_windows_protocol.py").exists() else script_dir.parent / "factory-overlay/usr/local/bin"
sys.path.insert(0, str(protocol_dir))

from omarchy_windows_protocol import Bridge, BridgeError, PNG_SIGNATURE, load_token, validate_png


TOKEN = "a" * 64
WINDOW = {"id": "window-1", "title": "Character Map", "process": "charmap.exe",
          "appGroup": "charmap", "width": 640, "height": 480, "fullscreen": False}
PNG_HEADER = PNG_SIGNATURE + struct.pack(">I", 13) + b"IHDR" + struct.pack(">II", 2, 3)


class Handler(BaseHTTPRequestHandler):
    seen = []

    def log_message(self, *_args):
        pass

    def _reply(self, content_type, data):
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        self.seen.append((self.path, self.headers.get("Authorization")))
        if self.path == "/v1/windows":
            self._reply("application/json", b'{"windows":[{"id":"window-1","title":"Character Map","process":"charmap.exe","appGroup":"charmap","width":640,"height":480,"fullscreen":false}]}')
        else:
            self._reply("image/png", PNG_HEADER)

    def do_POST(self):
        self.seen.append((self.path, self.headers.get("Authorization")))
        self.rfile.read(int(self.headers["Content-Length"]))
        self._reply("application/json", b'{"ok":true}')


class ProtocolTests(unittest.TestCase):
    def test_token_must_be_64_hex_digits(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "token"
            path.write_text(TOKEN + "\n")
            self.assertEqual(load_token(path), TOKEN)
            path.write_text("short")
            with self.assertRaises(BridgeError):
                load_token(path)

    def test_png_dimensions_and_size_are_bounded(self):
        self.assertEqual(validate_png(PNG_HEADER), (2, 3))
        with self.assertRaises(BridgeError):
            validate_png(PNG_SIGNATURE + struct.pack(">I", 13) + b"IHDR" + struct.pack(">II", 9000, 2))

    def test_authenticated_listing_frame_and_control(self):
        Handler.seen = []
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            bridge = Bridge(TOKEN, f"http://127.0.0.1:{server.server_address[1]}")
            self.assertEqual(bridge.windows(), [WINDOW])
            self.assertEqual(bridge.frame("window-1"), (PNG_HEADER, (2, 3)))
            bridge.input("window-1", {"type": "pointer", "x": 2, "y": 3})
            bridge.close("window-1")
            self.assertEqual(len(Handler.seen), 4)
            self.assertTrue(all(auth == f"Bearer {TOKEN}" for _, auth in Handler.seen))
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=2)


if __name__ == "__main__":
    unittest.main()
