// SPDX-License-Identifier: MIT
// A narrow binary sidecar around Wayseam's Windows Graphics Capture helper.
// Its only request is "capture <hex HWND>"; it has no script/command route.
using System;
using System.Globalization;
using System.IO;
using System.Text;
using System.Threading;

internal static class OmarchyWgcHost
{
    private const int MaximumFrameBytes = 60000000;

    public static int Main()
    {
        Stream output = Console.OpenStandardOutput();
        BinaryWriter writer = new BinaryWriter(output);
        bool supported = false;
        try { supported = WayseamWgcCapture.IsSupported(); }
        catch { }
        writer.Write(Encoding.ASCII.GetBytes("OWGC"));
        writer.Write((byte)(supported ? 1 : 0));
        writer.Flush();
        if (!supported) return 0;

        string line;
        while ((line = Console.ReadLine()) != null)
        {
            if (line == "quit") break;
            byte[] payload = null;
            try
            {
                string[] parts = line.Split(' ');
                ulong raw;
                if (parts.Length == 2 && parts[0] == "capture" &&
                    parts[1].Length > 0 && parts[1].Length <= 16 &&
                    ulong.TryParse(parts[1], NumberStyles.HexNumber,
                        CultureInfo.InvariantCulture, out raw))
                {
                    IntPtr hwnd = new IntPtr(unchecked((long)raw));
                    WayseamWgcCapture.DeltaCaptureResult frame = null;
                    // The first frame can arrive after the session starts.
                    // Give the frame pool a bounded warm-up before the Go
                    // bridge falls back to PrintWindow.
                    for (int attempt = 0; attempt < 4; attempt++)
                    {
                        frame = WayseamWgcCapture.CaptureDeltaFrame(
                            "omarchy-" + parts[1], hwnd, -1, 150);
                        if (frame != null && frame.Ok) break;
                        Thread.Sleep(30);
                    }
                    if (frame != null && frame.Ok && frame.Bytes != null &&
                        frame.Bytes.Length >= 36 &&
                        frame.Bytes.Length <= MaximumFrameBytes)
                        payload = frame.Bytes;
                }
            }
            catch { }
            writer.Write(payload == null ? -1 : payload.Length);
            if (payload != null) writer.Write(payload);
            writer.Flush();
        }
        return 0;
    }
}
