# Windows Graphics Capture helper provenance

`wayseam_wgc.cs` is adapted from
[Wayseam](https://github.com/goktugvatandas/wayseam),
`guest/oem/agent/wayseam_wgc.cs`, under the MIT license in
`WAYSEAM-LICENSE.txt`. The copy here retains its capture implementation. The
attempt to suppress Windows' capture indicator was removed, since that action
requires explicit user consent and a packaged app capability.

Wayseam credits WinPodX (Copyright 2026 Kim DaeHyun) for portions of its
Windows guest payload. Its MIT notice is included in `WAYSEAM-LICENSE.txt`.
`omarchy_wgc_host.cs` is a narrow local capture transport written for Omarchy.
