# Display matching

At each launch, Omarchy reads the current Windows monitor modes. In Immersive
(fullscreen) mode it starts the guest output at the monitor's pixel resolution.
The bundled WINQ-EMU runtime receives that monitor's refresh rate as a
millihertz `-display ... ,refresh-rate=` option, which it advertises through the
virtual GPU's EDID. Hyprland's QEMU profile selects the preferred virtual mode.
For example, a Windows monitor at 2560×1440 and 360 Hz supplies a 2560×1440
guest canvas and a 360,000 mHz virtual EDID refresh rate.

Windowed mode instead fits the guest canvas to the window's client area. Its
resolution is therefore smaller than the physical monitor when the Windows
taskbar, title bar, or window borders occupy space. Resizing the VM window can
change the guest resolution again through the guest's native display sync.
Immersive mode is the way to match the physical pixel count.

For multiple guest displays, the launcher sizes each initial output for its
assigned Windows monitor. The QEMU display refresh option is global, so every
virtual output advertises the first output's rate, even when Windows monitors
have different rates. Stock QEMU fallback does not support this WINQ-EMU
option; in that fallback the virtual refresh rate is not guaranteed to match.
Changing Windows monitor settings while Omarchy is running requires a relaunch
to reread the host mode. Windows' current display settings API reports an
integer rate, so fractional rates such as 59.94 Hz may be advertised as 60 Hz.
A matched virtual mode does not prove that every guest app produces frames at
that rate.

To verify on a particular PC, compare Windows **Settings → System → Display →
Advanced display** with `hyprctl monitors -j` inside Omarchy after launching
Immersive. Check the active width, height, and refresh rate for each output.
