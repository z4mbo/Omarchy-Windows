"""Offline capture comparison rejects mismatched or missing measurements."""

import csv
import importlib.util
from pathlib import Path
import tempfile
import unittest

module_path = Path(__file__).with_name("compare-presentmon.py")
spec = importlib.util.spec_from_file_location("compare_presentmon", module_path)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

HEADERS = ["Application", "ProcessID", "SwapChainAddress", "Resolution", "Runtime", "GPU",
           "TimeInSeconds", "MsBetweenPresents", "MsBetweenDisplayChange", "MsUntilDisplayed",
           "MsPCLatency", "Dropped", "GPU0Util(%)", "CPUUtil(%)"]


def capture(path: Path, frame_ms: float, resolution: str = "1280x720", input_metric: str = "NA"):
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=HEADERS)
        writer.writeheader()
        for i in range(100):
            writer.writerow({"Application": "blender.exe", "ProcessID": "123", "SwapChainAddress": "0x10",
                             "Resolution": resolution, "Runtime": "DXGI", "GPU": "RTX 5080",
                             "TimeInSeconds": str(i * frame_ms / 1000),
                             "MsBetweenPresents": str(frame_ms), "MsBetweenDisplayChange": str(frame_ms),
                             "MsUntilDisplayed": "2", "MsPCLatency": input_metric, "Dropped": "0",
                             "GPU0Util(%)": "40", "CPUUtil(%)": "20"})


class CaptureComparisonTest(unittest.TestCase):
    def test_intel_v1_metric_headers_from_physical_capture(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "intel.csv"
            capture(path, 16)
            content = path.read_text(encoding="utf-8")
            path.write_text(content.replace("MsBetween", "msBetween").replace("MsUntil", "msUntil"),
                            encoding="utf-8")
            result = module.read_capture(path, "blender.exe", 0, .5)
            self.assertEqual(result["present_frame_ms"]["median"], 16)
            self.assertEqual(result["display_frame_ms"]["median"], 16)
            self.assertEqual(result["present_to_display_ms"]["median"], 2)
            header, rows = path.read_text(encoding="utf-8").split("\n", 1)
            path.write_text(header + ",MsBetweenPresents\n" + rows, encoding="utf-8")
            with self.assertRaisesRegex(module.CaptureError, "ambiguous CSV columns"):
                module.read_capture(path, "blender.exe", 0, .5)

    def test_matching_capture_sets_report_frame_delta_without_inventing_input(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for i in range(2):
                capture(root / f"base{i}.csv", 16)
                capture(root / f"integrated{i}.csv", 20)
            before = [module.read_capture(root / f"base{i}.csv", "blender.exe", 0, .5) for i in range(2)]
            after = [module.read_capture(root / f"integrated{i}.csv", "blender.exe", 0, .5) for i in range(2)]
            result = module.compare(before, after)
            self.assertAlmostEqual(result["median_of_run_metrics"]["present_frame_ms.median"]["integrated_minus_standalone"], 4)
            self.assertIsNone(result["median_of_run_metrics"]["pc_latency_ms.median"])
            self.assertEqual(before[0]["pc_latency_coverage"], 0)

    def test_resolution_or_swapchain_mismatch_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            capture(root / "base.csv", 16)
            capture(root / "other.csv", 16, resolution="1920x1080")
            before = module.read_capture(root / "base.csv", "blender.exe", 0, .5)
            after = module.read_capture(root / "other.csv", "blender.exe", 0, .5)
            with self.assertRaisesRegex(module.CaptureError, "Resolution"):
                module.compare([before, before], [after, after])
            with (root / "base.csv").open("a", encoding="utf-8") as handle:
                handle.write("blender.exe,123,0x20,1280x720,DXGI,RTX 5080,2,16,16,2,NA,0,40,20\n")
            with self.assertRaisesRegex(module.CaptureError, "SwapChainAddress"):
                module.read_capture(root / "base.csv", "blender.exe", 0, .5)

    def test_optional_static_frame_pixel_comparison(self):
        try:
            from PIL import Image
        except ImportError:
            self.skipTest("Pillow unavailable")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first, second = root / "a.png", root / "b.png"
            Image.new("RGB", (4, 4), (1, 2, 3)).save(first)
            Image.new("RGB", (4, 4), (1, 2, 3)).save(second)
            result = module.image_difference(first, second, None)
            self.assertEqual(result["different_pixel_fraction"], 0)
            self.assertEqual(result["mean_absolute_channel_difference"], 0)


if __name__ == "__main__":
    unittest.main()
