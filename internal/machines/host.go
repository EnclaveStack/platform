package machines

import "runtime"

// The main machine's own system, reported like an agent's (MCH-05).
const (
	hostOS   = runtime.GOOS
	hostArch = runtime.GOARCH
)
