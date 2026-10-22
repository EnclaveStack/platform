package memory

import (
	"testing"

	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/messaging/messagingtest"
)

func TestConformance(t *testing.T) {
	t.Parallel()
	messagingtest.Run(t, func(*testing.T) messaging.Broker { return New() })
}
