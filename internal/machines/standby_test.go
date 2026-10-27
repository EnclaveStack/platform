package machines

import (
	"errors"
	"testing"
)

// A write while a copy is made may be missing from it, so the next tick copies again instead of taking
// the state after the copy as copied (ADR-057).
func TestCopiedState(t *testing.T) {
	last := fingerprint{1, 1, 0, 0}
	after := fingerprint{2, 2, 0, 0}
	now := func() fingerprint { return after }
	if got := copiedState(last, true, true, nil, now); got == after || got != (fingerprint{}) {
		t.Errorf("changed during the copy: %v", got)
	}
	if got := copiedState(last, true, false, nil, now); got != after {
		t.Errorf("copied: %v", got)
	}
	if got := copiedState(last, false, false, nil, now); got != after {
		t.Errorf("no standby: %v", got)
	}
	if got := copiedState(last, false, false, errors.New("down"), now); got != last {
		t.Errorf("failed with no standby: %v", got)
	}
}
