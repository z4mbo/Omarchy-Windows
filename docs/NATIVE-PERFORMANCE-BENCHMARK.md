# Native Windows app performance comparison

This is a prepared test procedure, not a benchmark result. No workload or
capture was launched while preparing it. The first useful comparison is the
installed Windows Blender 5.2 viewport run **standalone** and as a native
Windows tile over a disposable Omarchy candidate. Test a game separately after
the basic window, input, workspace, and fullscreen acceptance checks pass.

The host has `C:\Program Files\NVIDIA Corporation\FrameViewSDK\bin\PresentMon_x64.exe`
(file version 1.9.12728.0). Its local FrameView SDK README describes per-frame
`MsBetweenPresents`, `MsBetweenDisplayChange`, `MsUntilDisplayed`, optional
`MsPCLatency`, CPU/GPU utilization, and resolution. The matching upstream
[PresentMon 1.9 command-line reference](https://github.com/GameTechDev/PresentMon/blob/v1.9.0/README.md#command-line-options)
documents `-process_name`, `-output_file`, and `-timed`. Verify the bundled
binary's accepted options at collection time; do not change tracing privileges
or install another tool just to make this first comparison.

## Controlled capture

1. Save a small, deterministic Blender 5.2 `.blend` scene with a timed camera or
   viewport animation. Use the same file, Blender executable, graphics backend,
   viewport shading, camera, window client size, pixel resolution, display
   refresh, Windows scaling/HDR state, driver, and animation segment in both
   conditions. Record their exact versions and hashes. Let each condition warm
   before recording. Disable unrelated interactive loads and note CPU/GPU clocks,
   thermals, background tasks, and power mode.
2. Capture at least three 30–40 second runs per condition, alternating the order
   (for example standalone, integrated, integrated, standalone, then repeat).
   Use the same collector and process name `blender.exe` in both conditions.
   FrameView/PresentMon follows the **Windows application process**, not QEMU.
   Its upstream reference describes a timed capture like:

   ```powershell
   $collector = 'C:\Program Files\NVIDIA Corporation\FrameViewSDK\bin\PresentMon_x64.exe'
   & $collector -process_name blender.exe -output_file 'C:\captures\standalone-1.csv' -timed 40 -no_top
   ```

   Repeat with distinct paths for every run. Keep the actual CSVs and their
   hashes. A failed/empty capture is a failed run, not a zero-FPS result.
3. Record the exact host Windows build, NVIDIA driver, GPU, CPU, monitor mode,
   Blender version, PresentMon version, launcher SHA-256, QEMU runtime SHA-256,
   guest artifact SHA-256, guest compatibility revision, Omarchy version, native
   protocol/capabilities, and RAM/CPU policy. Capture `hyprctl monitors -j`
   during integrated runs. Preserve the pre-update version and the candidate
   version separately. Do not update the normal installed disk during this test.
4. On a paused, identical Blender frame, take host screenshots of the **app
   client area** at the same pixel size. A guest-only screenshot sees the proxy,
   not the directly drawn Windows pixels. Compare the same crop and visually
   inspect text, color, edges, cursor, overlays, and artifacts. The optional PNG
   comparison reports exact pixel difference; a different animation frame,
   color pipeline, or crop makes it inconclusive.
5. Run the offline analyzer after collection:

   ```powershell
   python3 scripts/perf/compare-presentmon.py `
     --application blender.exe `
     --standalone C:\captures\standalone-1.csv C:\captures\standalone-2.csv C:\captures\standalone-3.csv `
     --integrated C:\captures\integrated-1.csv C:\captures\integrated-2.csv C:\captures\integrated-3.csv `
     --output C:\captures\native-comparison.json
   ```

   The analyzer never starts Blender, QEMU, or the collector. It rejects mixed
   target PIDs/swap chains, a mismatched recorded resolution/runtime/GPU, short
   captures, and missing target frames. It reports each run, then the median of
   run metrics for present FPS, median/p95/p99 frame time, display pacing, and
   present-to-display delay when available. Optional GPU0/CPU utilization comes
   from the collector; it is system telemetry, not isolated QEMU overhead.

## Interpretation and additional acceptance

`MsBetweenPresents` describes present pacing; `MsBetweenDisplayChange` describes
displayed-frame pacing. `MsUntilDisplayed` starts at Present, so it is **not**
physical input-to-photon latency. FrameView's `MsPCLatency` is available only in
supported titles; `NA` must remain unavailable, never become zero or a guessed
input result. For Blender and most games, a controlled high-speed camera or
photodiode test is still needed for end-to-end input latency. Record normal text,
mouse, raw input, fullscreen, popup, Alt+F4, and Omarchy Super-chord behavior
separately from throughput. PresentMon's [OpenGL/Vulkan note](https://github.com/GameTechDev/PresentMon/blob/v1.9.0/README.md#analyzing-opengl-and-vulkan-applications)
also says some timing fields are less direct for runtimes reported as `Other`.

For updates, run this capture against a disposable upgraded candidate and add a
smoke check: launcher starts the existing guest version or performs a verified
upgrade; user files/settings remain; package revision and runtime receipt match;
Blender and a Linux app open; native grants, Super keys, lock, workspace hide,
fullscreen, close/revoke, and rollback work. Preserve the old disk and receipt
until the candidate is accepted. A successful fresh install alone does not show
that future updates preserve the user's system.

The Linux GPU path is measured separately. The existing WSL companion proof
only mapped a Blender window and ran CUDA in another process. Its next falsifiable
test is actual viewport navigation/model editing through Waypipe, then a CUDA
render triggered from that forwarded GUI with selected-device and image evidence.
Measure interaction latency and visual correctness there; do not mix WSL results
with the Windows-native app comparison. OptiX still failed in the previous lab.

No frame-time, quality, or input performance result is claimed by this document.
