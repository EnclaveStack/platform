package testenv

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Agent is another machine, in-process: a real agent over mutual TLS on loopback, with fake engines
// behind it (L5-002 §10).
type Agent struct {
	Name       string
	ID         string
	Driver     *fake.Driver
	Hypervisor *fake.Hypervisor
	StateDir   string
	Port       int

	t      testing.TB
	cancel context.CancelFunc
	done   chan struct{}
}

// AddAgent adds a machine through the API, joins an agent to it and waits until it is online.
func (e *Env) AddAgent(t testing.TB, c *client.Client, name string, labels map[string]string) *Agent {
	t.Helper()
	ctx := context.Background()
	added, err := c.AddMachine(ctx, api.AddMachine{Name: name, Labels: labels})
	if err != nil {
		t.Fatalf("add machine %s: %v", name, err)
	}
	a := e.JoinAgent(t, name, added.JoinCode)
	e.WaitMachine(t, c, name, api.MachineOnline)
	return a
}

// JoinAgent joins an in-process agent with a code and starts it; it doesn't wait for it to be online.
func (e *Env) JoinAgent(t testing.TB, name, code string) *Agent {
	t.Helper()
	ctx := context.Background()
	port := freePort(t)
	a := &Agent{Name: name, Driver: fake.New(0), Hypervisor: fake.NewHypervisor(), StateDir: filepath.Join(t.TempDir(), "agent-"+name), Port: port, t: t}
	id, err := agent.Join(ctx, agent.JoinOptions{Server: e.URL, Code: code, Advertise: "127.0.0.1",
		Listen: "127.0.0.1:" + strconv.Itoa(port), StateDir: a.StateDir})
	if err != nil {
		t.Fatalf("join %s: %v", name, err)
	}
	a.ID = id.MachineID
	a.Start()
	t.Cleanup(a.Stop)
	return a
}

// Start runs the agent (again, after Stop) on its saved identity.
func (a *Agent) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	a.cancel, a.done = cancel, make(chan struct{})
	go func() {
		defer close(a.done)
		err := agent.Run(ctx, agent.RunConfig{StateDir: a.StateDir, Driver: a.Driver, Hypervisor: a.Hypervisor,
			Log: slog.New(slog.DiscardHandler), Ready: ready, TunnelPublicKey: func(context.Context) string { return "key-" + a.Name }, Sweep: a.Driver.Sweep})
		if err != nil && ctx.Err() == nil {
			a.t.Errorf("agent %s: %v", a.Name, err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		a.t.Fatalf("agent %s didn't start", a.Name)
	}
}

// Stop stops the agent, as if its computer went away; its containers (in the fake engine) stay.
func (a *Agent) Stop() {
	if a.cancel == nil {
		return
	}
	a.cancel()
	<-a.done
	a.cancel = nil
}

// WaitMachine waits until a machine is in state.
func (e *Env) WaitMachine(t testing.TB, c *client.Client, name, state string) api.MachineSummary {
	t.Helper()
	var m api.MachineSummary
	Eventually(t, 10*time.Second, name+" "+state, func() bool {
		var err error
		m, err = c.GetMachineByName(context.Background(), name)
		return err == nil && m.State == state
	})
	return m
}

func freePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}
