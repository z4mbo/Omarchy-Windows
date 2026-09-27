package main

// The native foreground handoff requires the native presentation path and one
// SDL display. Keep this decision available to both host builds and tests.
func nativeForegroundExperimentSupported(cfg *config, presentation string) bool {
	return !cfg.experimentalNativeForeground || (presentation == "native" && cfg.displays == 1)
}
