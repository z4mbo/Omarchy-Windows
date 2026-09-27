#!/usr/bin/env python3
"""Compare existing FrameView/PresentMon per-frame CSVs without launching apps."""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import math
from pathlib import Path
import statistics


class CaptureError(ValueError):
    pass


def number(value: str | None) -> float | None:
    if value is None or value.strip().upper() in ("", "NA", "N/A", "NULL"):
        return None
    try:
        result = float(value)
    except ValueError:
        return None
    return result if math.isfinite(result) else None


def percentile(values: list[float], fraction: float) -> float:
    ordered = sorted(values)
    position = (len(ordered) - 1) * fraction
    low = math.floor(position)
    high = math.ceil(position)
    return ordered[low] + (ordered[high] - ordered[low]) * (position - low)


def distribution(values: list[float]) -> dict | None:
    if not values:
        return None
    return {"samples": len(values), "mean": statistics.fmean(values),
            "median": statistics.median(values), "p95": percentile(values, .95),
            "p99": percentile(values, .99)}


def _identity(rows: list[dict], column: str) -> str:
    values = {(row.get(column) or "").strip() for row in rows if (row.get(column) or "").strip()}
    if len(values) > 1:
        raise CaptureError(f"Capture mixes {column} values: {sorted(values)}")
    return next(iter(values), "")


def read_capture(path: Path, application: str, warmup: float, minimum: float) -> dict:
    if not path.is_file() or path.stat().st_size > 128 * 1024 * 1024:
        raise CaptureError(f"Missing or oversized capture: {path}")
    with path.open("r", encoding="utf-8-sig", newline="") as handle:
        reader = csv.DictReader(handle)
        if not reader.fieldnames:
            raise CaptureError(f"Missing CSV header: {path}")
        # Intel's --v1_metrics spells these columns msBetweenPresents, while
        # FrameView exports MsBetweenPresents. Preserve the metric meanings.
        aliases = {name.casefold(): name for name in (
            "MsBetweenPresents", "MsBetweenDisplayChange", "MsUntilDisplayed", "MsPCLatency")}
        reader.fieldnames = [aliases.get(name.strip().casefold(), name.strip())
                            for name in reader.fieldnames]
        if len(set(reader.fieldnames)) != len(reader.fieldnames):
            raise CaptureError(f"{path}: duplicate or ambiguous CSV columns")
        required = {"Application", "ProcessID", "SwapChainAddress", "MsBetweenPresents"}
        missing = required.difference(reader.fieldnames)
        if missing:
            raise CaptureError(f"{path}: missing frame columns {sorted(missing)}")
        rows = [row for row in reader if (row.get("Application") or "").strip().casefold() == application.casefold()]
    if not rows:
        raise CaptureError(f"{path}: no frames for {application}")
    process = _identity(rows, "ProcessID")
    swapchain = _identity(rows, "SwapChainAddress")
    if not process or not swapchain:
        raise CaptureError(f"{path}: target process or swap chain is unidentified")
    initial = next((number(row.get("TimeInSeconds")) for row in rows
                    if number(row.get("TimeInSeconds")) is not None), None)
    if warmup and initial is None:
        raise CaptureError(f"{path}: TimeInSeconds is needed to trim warmup")
    if initial is not None:
        rows = [row for row in rows if (value := number(row.get("TimeInSeconds"))) is not None
                and value >= initial + warmup]
    frames = [value for row in rows if (value := number(row.get("MsBetweenPresents"))) is not None and value > 0]
    if len(frames) < 60:
        raise CaptureError(f"{path}: fewer than 60 usable target frames")
    times = [value for row in rows if (value := number(row.get("TimeInSeconds"))) is not None]
    duration = (max(times) - min(times)) if len(times) > 1 else sum(frames) / 1000
    if duration < minimum:
        raise CaptureError(f"{path}: {duration:.2f}s usable duration is below {minimum:.2f}s")
    metadata = {name: _identity(rows, name) for name in ("Resolution", "Runtime", "GPU")}
    modes = sorted({row.get("PresentMode", "").strip() for row in rows if row.get("PresentMode", "").strip()})
    displayed = [value for row in rows if (value := number(row.get("MsBetweenDisplayChange"))) is not None and value > 0]
    present_to_display = [value for row in rows if (value := number(row.get("MsUntilDisplayed"))) is not None and value > 0]
    pc_latency = [value for row in rows if (value := number(row.get("MsPCLatency"))) is not None and value > 0]
    dropped = [number(row.get("Dropped")) for row in rows]
    dropped = [value for value in dropped if value is not None]
    gpu_util = [value for row in rows if (value := number(row.get("GPU0Util(%)"))) is not None]
    cpu_util = [value for row in rows if (value := number(row.get("CPUUtil(%)"))) is not None]
    return {
        "file": str(path.resolve()), "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
        "application": application, "process_id": process, "swap_chain": swapchain,
        "duration_seconds": duration, "frames": len(frames), "metadata": metadata,
        "present_modes": modes, "present_frame_ms": distribution(frames),
        "present_fps": 1000 / statistics.fmean(frames),
        "display_frame_ms": distribution(displayed),
        "display_frame_coverage": len(displayed) / len(rows),
        "present_to_display_ms": distribution(present_to_display),
        "present_to_display_coverage": len(present_to_display) / len(rows),
        "pc_latency_ms": distribution(pc_latency),
        "pc_latency_coverage": len(pc_latency) / len(rows),
        "dropped_fraction": sum(value == 1 for value in dropped) / len(dropped) if dropped else None,
        "gpu0_utilization_percent_mean": statistics.fmean(gpu_util) if gpu_util else None,
        "cpu_utilization_percent_mean": statistics.fmean(cpu_util) if cpu_util else None,
    }


def image_difference(standalone: Path, integrated: Path, crop: tuple[int, int, int, int] | None) -> dict:
    try:
        from PIL import Image, ImageChops, ImageStat
    except ImportError as exc:
        raise CaptureError("Pillow is required for optional image comparison") from exc
    with Image.open(standalone) as original, Image.open(integrated) as projected:
        if original.size != projected.size:
            raise CaptureError("Image dimensions differ; capture the same app area at the same resolution")
        if crop:
            x, y, width, height = crop
            if x < 0 or y < 0 or width <= 0 or height <= 0 or x + width > original.width or y + height > original.height:
                raise CaptureError("Image crop is outside the captured image")
            box = (x, y, x + width, y + height)
            original = original.crop(box)
            projected = projected.crop(box)
        difference = ImageChops.difference(original.convert("RGB"), projected.convert("RGB"))
        channels = ImageStat.Stat(difference).mean
        red, green, blue = difference.split()
        changed = ImageChops.lighter(ImageChops.lighter(red, green), blue)
        nonzero = sum(changed.histogram()[1:])
        return {"pixels": original.width * original.height,
                "mean_absolute_channel_difference": statistics.fmean(channels),
                "different_pixel_fraction": nonzero / (original.width * original.height),
                "standalone_sha256": hashlib.sha256(standalone.read_bytes()).hexdigest(),
                "integrated_sha256": hashlib.sha256(integrated.read_bytes()).hexdigest()}


def compare(standalone: list[dict], integrated: list[dict]) -> dict:
    if len(standalone) != len(integrated) or len(standalone) < 2:
        raise CaptureError("Provide matching sets of at least two standalone and integrated runs")
    runs = standalone + integrated
    missing_metadata = []
    for key in ("Resolution", "Runtime", "GPU"):
        present = [bool(item["metadata"][key]) for item in runs]
        if any(present) and not all(present):
            raise CaptureError(f"Some runs omit {key}; record it consistently")
        if not any(present):
            missing_metadata.append(key)
        values = {item["metadata"][key] for item in runs if item["metadata"][key]}
        if len(values) > 1:
            raise CaptureError(f"Runs differ in {key}: {sorted(values)}")
    metrics = ("present_fps", "present_frame_ms", "display_frame_ms", "present_to_display_ms",
               "pc_latency_ms", "gpu0_utilization_percent_mean", "cpu_utilization_percent_mean")
    summary = {}
    for metric in metrics:
        for field in (("median", "p95", "p99") if metric.endswith("_ms") else (None,)):
            name = f"{metric}.{field}" if field else metric
            def values(runs):
                return [value for run in runs if (value := (run[metric].get(field) if field and run[metric]
                                                        else run[metric] if not field else None)) is not None]
            before, after = values(standalone), values(integrated)
            if len(before) != len(standalone) or len(after) != len(integrated):
                summary[name] = None
                continue
            baseline, candidate = statistics.median(before), statistics.median(after)
            summary[name] = {"standalone": baseline, "integrated": candidate,
                             "integrated_minus_standalone": candidate - baseline,
                             "percent_change": 100 * (candidate / baseline - 1) if baseline else None}
    return {"standalone_runs": standalone, "integrated_runs": integrated,
            "missing_csv_metadata": missing_metadata,
            "median_of_run_metrics": summary,
            "interpretation": "Positive frame-time delta is worse; positive FPS delta is better. No equivalence or performance claim is inferred."}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--standalone", type=Path, nargs="+", required=True)
    parser.add_argument("--integrated", type=Path, nargs="+", required=True)
    parser.add_argument("--application", required=True, help="exact process name, e.g. blender.exe")
    parser.add_argument("--warmup-seconds", type=float, default=0)
    parser.add_argument("--min-seconds", type=float, default=15)
    parser.add_argument("--standalone-image", type=Path)
    parser.add_argument("--integrated-image", type=Path)
    parser.add_argument("--crop", help="identical x,y,width,height rectangle in both PNG files")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.warmup_seconds < 0 or args.min_seconds <= 0:
            raise CaptureError("Durations must be positive")
        baseline = [read_capture(path, args.application, args.warmup_seconds, args.min_seconds)
                    for path in args.standalone]
        integrated = [read_capture(path, args.application, args.warmup_seconds, args.min_seconds)
                      for path in args.integrated]
        report = compare(baseline, integrated)
        if bool(args.standalone_image) != bool(args.integrated_image):
            raise CaptureError("Provide both static-frame PNG images or neither")
        if args.standalone_image:
            crop = tuple(int(value) for value in args.crop.split(",")) if args.crop else None
            if crop and len(crop) != 4:
                raise CaptureError("Crop must be x,y,width,height")
            report["static_frame_image_difference"] = image_difference(args.standalone_image,
                                                                         args.integrated_image, crop)
        args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    except (CaptureError, OSError, ValueError) as exc:
        parser.exit(2, f"Comparison refused: {exc}\n")
    print(args.output.resolve())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
