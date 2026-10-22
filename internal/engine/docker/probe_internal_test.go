package docker

import (
	"errors"
	"testing"

	"github.com/enclavestack/platform/internal/engine"
)

// A failure to start the health-check helper keeps its own class: a machine waiting for addresses is
// retried, not reported as the revision failing its check.
func TestAProbeSetupFailureKeepsItsClass(t *testing.T) {
	waiting := &probeSetupError{errors.New("this machine has no free addresses left in 10.0.0.0/24; waiting for the main machine to give it more")}
	if engine.IsPermanent(waiting) {
		t.Error("a wait became permanent")
	}
	if !engine.IsPermanent(&probeSetupError{engine.Permanent(errors.New("no such image"))}) {
		t.Error("a permanent cause lost its class")
	}
	var setup *probeSetupError
	if !errors.As(error(waiting), &setup) || waiting.Error() != "start the health-check helper: "+waiting.err.Error() {
		t.Errorf("%v", waiting)
	}
}
