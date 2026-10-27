package httpapi_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/machines"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// The standby control plane (ADR-057, L2-016 SBY-01 – SBY-06): copies reach the standby, promotion
// makes it the main machine with everything the old one knew, and the old one is fenced off.
func TestStandbyCopiesPromotesAndFencesTheOldMain(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.StandbyEvery = 100 * time.Millisecond }))
	c, _ := env.Token(t, "")
	ctx := context.Background()
	worker := env.AddAgent(t, c, "rack-1", nil)

	added, err := c.AddMachine(ctx, api.AddMachine{Name: "spare", Role: api.MachineStandby})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(added.JoinCommand, "--standby") || added.Machine.Schedulable {
		t.Errorf("added %+v", added)
	}
	if _, err := c.AddMachine(ctx, api.AddMachine{Name: "spare-2", Role: api.MachineStandby}); client.Code(err) != api.CodeStandbyExists {
		t.Errorf("second standby: %v", err)
	}
	sb := env.JoinAgent(t, "spare", added.JoinCode)
	env.WaitMachine(t, c, "spare", api.MachineOnline)
	if _, err := c.UpdateMachineByName(ctx, "spare", api.UpdateMachine{Schedulable: ptr(true)}); client.Code(err) != api.CodeValidationFailed {
		t.Errorf("a schedulable standby: %v", err)
	}

	// Something only the control plane knows, then a copy that holds it.
	sec, err := c.CreateResource(ctx, api.KindSecret, "stripe-key", api.SecretSpec{Value: "sk_live_1"})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, c, sec.ID)
	var m api.MachineSummary
	testenv.Eventually(t, 10*time.Second, "a copy after the secret", func() bool {
		m, err = c.GetMachineByName(ctx, "spare")
		return err == nil && m.Standby != nil && m.Standby.LastCopyAt != nil && m.Standby.LastCopyAt.After(sec.CreatedAt) && !m.Standby.Stale
	})
	if m.Standby.LastCopyBytes == 0 || m.Standby.LastCopyError != "" {
		t.Errorf("standby %+v", m.Standby)
	}

	// Promote on the standby, into an empty data directory.
	data := filepath.Join(t.TempDir(), "promoted")
	p, err := machines.Promote(ctx, sb.StateDir, data, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if p.Machine != "spare" || p.Epoch != 1 {
		t.Errorf("promotion %+v", p)
	}
	if _, err := machines.Promote(ctx, sb.StateDir, data, time.Now()); err == nil {
		t.Error("promoted twice into the same data directory")
	}
	if _, err := machines.Promote(ctx, worker.StateDir, filepath.Join(t.TempDir(), "x"), time.Now()); err == nil {
		t.Error("promoted a worker")
	}

	// The worker's agent follows the new epoch: the old control plane is refused, and stops.
	worker.Stop()
	if err := os.WriteFile(filepath.Join(worker.StateDir, "epoch"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worker.Start()
	testenv.Eventually(t, 10*time.Second, "the old control plane is replaced", func() bool {
		_, err := c.ListMachines(ctx)
		return client.Code(err) == api.CodeControlPlaneReplaced
	})
	res, err := http.Get(env.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz %d %s", res.StatusCode, body)
	}

	// The promoted control plane starts with everything: the spare is the main machine, the secret is there.
	env.Stop()
	promoted := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.DataDir = data }))
	pc, _ := promoted.Token(t, "")
	ms, err := pc.ListMachines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, mm := range ms {
		switch mm.Name {
		case "spare":
			if !mm.Main || mm.Role != api.MachineWorker {
				t.Errorf("spare %+v", mm)
			}
		case "rack-1":
		default:
			if mm.Main || mm.State != api.MachineOffline {
				t.Errorf("old main %+v", mm)
			}
		}
	}
	if _, err := pc.GetResource(ctx, sec.ID); err != nil {
		t.Errorf("the secret didn't come across: %v", err)
	}
}
