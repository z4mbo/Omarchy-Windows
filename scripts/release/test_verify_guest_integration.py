import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('verify_guest_integration',
    Path(__file__).with_name('verify-guest-integration.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class PackagedIntegrationTests(unittest.TestCase):
    def test_stale_and_missing_packaged_files_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source, guest = root / 'source', root / 'guest'
            source.mkdir()
            pairs = (
                ('omarchy-windows-open', 'usr/local/bin/omarchy-windows-open'),
                ('export-seamless-token', 'usr/local/lib/try-omarchy/export-seamless-token'),
                ('try-omarchy-seamless-token.service', 'etc/systemd/system/try-omarchy-seamless-token.service'),
            )
            for name, relative in pairs:
                (source / name).write_bytes(b'current\r\n')
                target = guest / 'factory-overlay' / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes(b'current\n')
            self.assertEqual(module.verify(source, guest), [])
            packaged = guest / 'factory-overlay/usr/local/bin/omarchy-windows-open'
            packaged.write_bytes(b'stale\n')
            self.assertEqual(len(module.verify(source, guest)), 1)
            packaged.unlink()
            self.assertIn('missing', module.verify(source, guest)[0])
            (source / 'omarchy_windows_new_helper.py').write_bytes(b'new')
            self.assertEqual(len(module.verify(source, guest)), 2)


if __name__ == '__main__':
    unittest.main()
