# Native Blender frame capture — 2026-09-27

The prepared Blender 5.2 `OmarchyViewportBaseline.blend` scene was open on the Windows host in a fullscreen 2560 × 1440 viewport. It contains 1,600 animated cubes, uses solid object colors, and loops frames 1–240 at a scene setting of 60 fps. The saved scene SHA-256 was `b5a912eb4606b945363b302baa3af3fd44ac52aa90f44fffaa1813bf9b9f0675`. The viewport visibly played, but its scene setting and on-screen FPS indicator are **not a measured frame-time baseline**.

The installed NVIDIA FrameView SDK `PresentMon_x64.exe` reported file version `1.9.12728.0`; its help listed the requested `--process_id`, `--process_name`, `--timed`, `--terminate_after_timed`, `--v1_metrics`, and CSV output options. With Blender PID 31048 live, the first 40-second process-ID capture exited immediately without creating `standalone-1.csv`. A short process-name retry also produced no CSV. A redirected five-second probe returned exit code 1 with empty stdout and stderr. The current unelevated token was neither an administrator nor a member of Performance Log Users, but the collector emitted no access-denied message, so this does not establish the failure cause.

After the user approved a recorder UAC prompt, a direct `Start-Process -Verb RunAs` invocation reported collector PID 31884. That process also exited immediately without a CSV or a matching Application event. A subsequent diagnostic PowerShell `.ps1` wrapper did not run: Windows PowerShell explicitly rejected it because script execution is disabled. No execution-policy, user-group, ETW, or other Windows security setting was changed.

At this point, the offline `scripts/perf/test_compare_presentmon.py` suite passed 3 tests against synthetic CSV fixtures, but no real frame data had been obtained.

## Intel collector validation

The official standalone [Intel PresentMon 2.6.0 release](https://github.com/GameTechDev/PresentMon/releases/tag/v2.6.0) was then downloaded. Its executable SHA-256 matched the release asset digest, `b2a706bc6ad475749e3b7e3409263aa1e6906d45bdcf993f6dbc0f660188f1af`, and Authenticode reported a valid Intel Corporation signature. No service or MSI was installed. An unelevated probe explicitly returned access denied with exit code 6. The user approved a direct recorder UAC prompt; no security setting was changed.

The collector started at 18:26 local time with the exact Blender PID, a separate ETW session name, a 10-second delay, a 40-second capture, `--terminate_after_timed`, `--no_console_stats`, and `--v1_metrics`. Computer Use observed the same saved benchmark scene playing in a fullscreen Windows Blender window before and after capture. The disposable Omarchy VM was stopped. The resulting `standalone-intel-1.csv` was 628,814 bytes, SHA-256 `3aeb722769ac8d37cdbb7c342bb590ae5e97821551326326f687d2f2d7cc6a04`.

The analyzer now accepts Intel's lowercase `msBetweenPresents`, `msBetweenDisplayChange`, and `msUntilDisplayed` spellings as the same metrics as their FrameView capitalization, and rejects ambiguous duplicate columns. Four analyzer tests passed, and the real CSV parsed successfully. After trimming five seconds:

| Measurement | Observed value |
| --- | --- |
| Retained duration / frames | 34.967 seconds / 2,099 |
| Application / runtime | Blender / DXGI, one swap chain |
| Present mode | Hardware Composed: Independent Flip |
| Present interval mean / p95 / p99 | 16.667 / 17.720 / 18.186 ms |
| Display interval mean / p95 / p99 | 16.667 / 19.447 / 19.545 ms |
| Present-to-display mean / p95 / p99 | 1.178 / 2.882 / 3.301 ms |
| Display timing coverage / dropped fraction | 100% / 0 |

This is **collector validation**, not a standalone-versus-Omarchy comparison. The scene was configured for 60 fps; the measured mean was about 60 fps. No matching integrated capture was made, and uninterrupted foreground conditions were not independently established throughout the capture. Present-to-display timing is not input latency. Input latency, CPU/GPU utilization, and CSV resolution/GPU metadata were unavailable. The benchmark scene was closed without saving playback-position changes.

The paired workload comparison in [NATIVE-PERFORMANCE-BENCHMARK.md](../NATIVE-PERFORMANCE-BENCHMARK.md) remains pending a working integrated candidate and controlled capture pairs. No native-equivalent performance claim follows from this run.
