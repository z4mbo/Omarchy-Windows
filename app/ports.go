package main

// The QMP values identify private socket roles and remain reserved for older
// installations. Clipboard, lifecycle and agent services use loopback TCP.
const (
	qmpToolsPort  = 4445
	qmpFwdPort    = 4446
	qmpSupPort    = 4447
	qmpNativePort = 4454 // private AF_UNIX monitor for native foreground permission
	clipPushPort  = 4448
	clipPullPort  = 4449
	lifecyclePort = 4450
	agentPort     = 4451
	transferPort  = 4452
	cameraPort    = 4453
)
