# WSL GPU app forwarded into disposable Omarchy — 2026-09-26

This is a bounded engineering experiment, **not a shipped GPU backend or a Windows-app compatibility test**. It asks whether an individual Linux app running in WSL can appear as a normal Hyprland client inside the existing QEMU Omarchy desktop. It does not make the complete Omarchy desktop run in WSL or provide native GPU passthrough to QEMU.

## Isolation and transport

- Host: the Windows 11 / RTX 5080 machine in [WSL-GPU-EXPERIMENT.md](../WSL-GPU-EXPERIMENT.md). The Linux process ran in the existing `TryOmarchy-GPU-Lab` WSL distribution. The Omarchy compositor ran in a fresh disposable QCOW2 overlay backed by the verified CI guest from [FRESH-GUEST-CANDIDATE-2026-09-26.md](FRESH-GUEST-CANDIDATE-2026-09-26.md).
- The Windows QEMU command used WHPX, 4 vCPUs, 4 GiB RAM, `-display none`, a 1280×720 virtual GPU output, and host-loopback SSH forwarding on port 2255. It did not open a Windows desktop window. The installed Omarchy disk, launcher, and settings were not accessed.
- A temporary key-only SSH server listened on the WSL lab's private `eth0` address, port 2227. A temporary `waypipeprobe` account and guest-generated test key carried Waypipe's Unix socket forwarding. [Waypipe 0.11.2](https://man.archlinux.org/man/waypipe.1.en) ran on both sides with `--no-gpu`; the remote display was a test socket under `/run/user/1001`, separate from WSLg's display. No host password, driver, Windows security, or `.wslconfig` change was used.
- Arch package sync installed Waypipe in the disposable guest and performed a full upgrade plus Waypipe installation **only in the WSL GPU lab**. The original Arch WSL distro was not changed.

## Observed result

| Check | Observation |
| --- | --- |
| Small WSL Wayland client | Zenity appeared in guest `hyprctl clients -j` as a separate mapped, visible, non-XWayland window on workspace 1. It floated as a dialog. |
| WSL Blender main window | Blender 5.2.2 LTS appeared concurrently as a separate mapped, visible, non-XWayland Hyprland client on workspace 1 with `floating: false`. It remained mapped after the CUDA render. [Raw guest client metadata](WAYPIPE-GUEST-CLIENTS-2026-09-26.json). |
| CUDA compute while GUI mapped | A **separate WSL Blender background process**, not an action triggered through the forwarded GUI, rendered a 64×64 Cycles scene with four samples. It selected `NVIDIA GeForce RTX 5080` CUDA, explicitly disabled the `Intel Core i9-14900KF` CPU device, exited 0, and saved a 5,054-byte PNG. [Raw render log](WAYPIPE-CUDA-RENDER-2026-09-26.txt). |
| Render output | SHA-256 `5a13dfceeb08977c95dcc1a1c817981f92d0ddccc58618706e793a8b42645c95`. The saved [test image](waypipe-cuda-render-2026-09-26.png) matched that digest when copied from WSL. |

The WSL render command was `blender --factory-startup --background --python-exit-code 7 --python render.py`, with `LD_LIBRARY_PATH=/usr/lib/wsl/lib` and `GALLIUM_DRIVER=d3d12` scoped to that process. The script used the same device-selection logic as [`scripts/gpu/probe.py`](../../scripts/gpu/probe.py): require a CUDA device, set every CPU device's `use` to false, render through Cycles `GPU`, and require a nonempty PNG. The full device list and result are in the raw log.

The forwarded Blender process logged Mesa EGL driver/screen warnings and a nonfatal `--noaudio` argument warning. Its mapping in Hyprland proves window registration, **not** usable viewport acceleration or visual correctness. We did not send or observe user input, manipulate a project in the GUI, test fullscreen or monitor refresh, measure latency or frame rate, or test Windows gaming load. `--no-gpu` blocks Wayland DMA-buffer forwarding; CUDA computation is a separate API and its passing render does not imply a fast 3D viewport. OptiX initialization still warned with error 7805; no OptiX render passed. This path does not run or validate Windows native applications inside Omarchy.

## Cleanup and retained artifacts

The forwarded processes were stopped and the disposable guest powered off cleanly (`reboot: Power down`); QEMU PID 22936 exited. The dedicated Windows `wsl.exe` process for the lab SSH server exited, the temporary WSL account and lab SSH keys/configuration were removed, and port 2227 had no listener. The preexisting host probe key used to reach the disposable guest was neither created nor changed by this experiment. Waypipe and the full package updates remain only in the separate WSL GPU lab.

An automatic approval review rejected deletion of the now-inactive disposable `gpu-test.qcow2` overlay under `%LOCALAPPDATA%\Temp\omarchy-gpu-waypipe-20260926`; the tool reported only `blocked by policy`. No alternate deletion was attempted. The overlay contains the guest-generated disposable private key, but its matching WSL account and authorized key have been removed. The original installed Omarchy disk remains separate.
