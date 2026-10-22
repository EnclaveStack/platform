package cli

import (
	"testing"

	"github.com/enclavestack/platform/pkg/api"
)

// The generic verbs (get, list, delete) name every kind by its own name; v0.6 shipped without them once.
func TestEveryKindHasAGenericName(t *testing.T) {
	for _, k := range api.Kinds {
		if got, err := parseKind(string(k)); err != nil || got != k {
			t.Errorf("parseKind(%q) = %q, %v", k, got, err)
		}
	}
}
