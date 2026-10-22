// Package machines keeps track of the machines of one EnclaveStack (ADR-042 – ADR-046): their certificate
// authority, the control plane's client certificate, a connection to each agent, and the health checks that
// turn machines online and offline. Manager implements the engine router's Machines port.
package machines

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/secretbox"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/pkg/api"
)

// Health-check policy (ADR-046, L3-007 §13).
const (
	DefaultInterval     = 10 * time.Second
	DefaultCheckTimeout = 5 * time.Second
	DefaultOfflineAfter = 30 * time.Second
)

// caAAD binds the sealed CA key to its purpose.
var caAAD = []byte("machine-ca")

// Manager knows every machine.
type Manager struct {
	Store   *store.Store
	Box     *secretbox.Box
	Local   engine.Driver
	LocalHV engine.Hypervisor
	Now     func() time.Time
	Log     *slog.Logger
	// Interval, CheckTimeout and OfflineAfter tune health checks; zero uses the defaults.
	Interval     time.Duration
	CheckTimeout time.Duration
	OfflineAfter time.Duration
	// Changed is told when a machine turns online or offline.
	Changed func(machineID, state string)
	// TunnelAddress and TunnelPort are where other machines reach the main machine's tunnel (ADR-045);
	// without an address the other machines can't reach it.
	TunnelAddress string
	TunnelPort    int

	mu      sync.RWMutex
	ca      *agent.CA
	cpCert  tls.Certificate
	cpUntil time.Time
	remotes map[string]*remote
}

// remote is the connection to one agent and what the last check found.
type remote struct {
	client *agent.Remote
	serial string
	addr   string
	port   int
	online bool
	since  time.Time
	info   agent.Info
}

func (m *Manager) init() {
	if m.remotes == nil {
		m.remotes = map[string]*remote{}
	}
}

func (m *Manager) interval() time.Duration { return orDefault(m.Interval, DefaultInterval) }

func (m *Manager) offlineAfter() time.Duration { return orDefault(m.OfflineAfter, DefaultOfflineAfter) }

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Engines implements cluster.Machines.
func (m *Manager) Engines(machineID string) (engine.Driver, engine.Hypervisor, error) {
	if machineID == "" || machineID == api.MainMachine {
		return m.Local, m.LocalHV, nil
	}
	m.mu.RLock()
	var client *agent.Remote
	var since time.Time
	online := false
	if r := m.remotes[machineID]; r != nil {
		client, since, online = r.client, r.since, r.online
	}
	m.mu.RUnlock()
	if !online || client == nil {
		name, when := m.describeOffline(machineID, since)
		return nil, nil, fmt.Errorf("%w: %s is offline%s", engine.ErrMachineOffline, name, when)
	}
	return client, client, nil
}

func (m *Manager) describeOffline(id string, since time.Time) (string, string) {
	name := id
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if mc, err := m.Store.GetMachine(ctx, id); err == nil {
		name = mc.Name
		if mc.State == api.MachineWaiting {
			return name, " (it hasn't joined yet)"
		}
		return name, " since " + mc.StateSince.Local().Format("15:04 on 2 Jan")
	}
	if !since.IsZero() {
		return name, " since " + since.Local().Format("15:04 on 2 Jan")
	}
	return name, ""
}

// OnlineMachines implements cluster.Machines.
func (m *Manager) OnlineMachines() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := []string{api.MainMachine}
	var others []string
	for id, r := range m.remotes {
		if r.online {
			others = append(others, id)
		}
	}
	sort.Strings(others)
	return append(out, others...)
}

// MachineOf implements cluster.Machines.
func (m *Manager) MachineOf(ctx context.Context, resourceID string) string {
	r, err := m.Store.GetResource(ctx, resourceID)
	if err != nil {
		return api.MainMachine
	}
	return r.MachineID()
}

// Online reports whether a machine takes calls now.
func (m *Manager) Online(machineID string) bool {
	if machineID == api.MainMachine {
		return true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	r := m.remotes[machineID]
	return r != nil && r.online
}

// AgentInfo is what a machine's agent last reported, and whether it did.
func (m *Manager) AgentInfo(machineID string) (agent.Info, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r := m.remotes[machineID]
	if r == nil || r.info.MachineID == "" {
		return agent.Info{}, false
	}
	return r.info, true
}

// CA returns the certificate authority, making it the first time (ADR-043).
func (m *Manager) CA(ctx context.Context) (*agent.CA, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.caLocked(ctx)
}

func (m *Manager) caLocked(ctx context.Context) (*agent.CA, error) {
	if m.ca != nil {
		return m.ca, nil
	}
	row, err := m.Store.GetMachineCA(ctx)
	if errors.Is(err, store.ErrNotFound) {
		certPEM, keyPEM, err := agent.NewCA(m.Now())
		if err != nil {
			return nil, err
		}
		sealed, err := m.Box.Seal(keyPEM, caAAD)
		if err != nil {
			return nil, err
		}
		err = m.Store.PutMachineCA(ctx, store.MachineCA{CertPEM: string(certPEM), KeyCiphertext: sealed, CreatedAt: m.Now()})
		if errors.Is(err, store.ErrConflict) {
			return m.caLocked(ctx) // made meanwhile by another request
		}
		if err != nil {
			return nil, err
		}
		row = store.MachineCA{CertPEM: string(certPEM), KeyCiphertext: sealed}
	} else if err != nil {
		return nil, err
	}
	keyPEM, err := m.Box.Open(row.KeyCiphertext, caAAD)
	if err != nil {
		return nil, errors.New("the machines' certificate authority can't be decrypted with this machine's key")
	}
	ca, err := agent.ParseCA([]byte(row.CertPEM), keyPEM)
	if err != nil {
		return nil, err
	}
	m.ca = ca
	return ca, nil
}

// clientCert is the control plane's certificate, renewed before its 30 days are over.
func (m *Manager) clientCert(ctx context.Context) (tls.Certificate, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ca, err := m.caLocked(ctx)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	if m.cpUntil.IsZero() || m.Now().After(m.cpUntil) {
		cert, err := ca.ControlPlaneCertificate(m.Now())
		if err != nil {
			return tls.Certificate{}, nil, err
		}
		m.cpCert, m.cpUntil = cert, m.Now().Add(20*24*time.Hour)
		m.dropClientsLocked()
	}
	return m.cpCert, ca.PEM, nil
}

func (m *Manager) dropClientsLocked() {
	for id, r := range m.remotes {
		r.client.Close()
		m.remotes[id] = &remote{online: r.online, since: r.since, info: r.info}
	}
}

// client returns the connection to a machine's agent, made again when its address, port or certificate changed.
func (m *Manager) client(ctx context.Context, mc store.Machine) (*remote, error) {
	cert, caPEM, err := m.clientCert(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r := m.remotes[mc.ID]
	if r != nil && r.client != nil && r.serial == mc.CertSerial && r.addr == mc.Address && r.port == mc.AgentPort {
		return r, nil
	}
	cfg, err := agent.ClientConfig(cert, caPEM, mc.ID, mc.CertSerial)
	if err != nil {
		return nil, err
	}
	next := &remote{client: agent.NewRemote(mc.ID, mc.Address, mc.AgentPort, cfg), serial: mc.CertSerial, addr: mc.Address, port: mc.AgentPort}
	if r != nil {
		if r.client != nil {
			r.client.Close()
		}
		next.online, next.since, next.info = r.online, r.since, r.info
	}
	m.remotes[mc.ID] = next
	return next, nil
}

// Forget drops a removed machine's connection.
func (m *Manager) Forget(machineID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.remotes[machineID]; r != nil && r.client != nil {
		r.client.Close()
	}
	delete(m.remotes, machineID)
}

// Run checks every machine every Interval until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	m.CheckAll(ctx)
	t := time.NewTicker(m.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.CheckAll(ctx)
		}
	}
}

// CheckAll runs one round of health checks (exported for tests).
func (m *Manager) CheckAll(ctx context.Context) {
	m.refreshMain(ctx)
	all, err := m.Store.ListMachines(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.Log.Error("list machines failed", "error", err)
		}
		return
	}
	var wg sync.WaitGroup
	for _, mc := range all {
		if mc.ID == api.MainMachine || mc.State == api.MachineWaiting {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.check(ctx, mc)
		}()
	}
	wg.Wait()
}

// refreshMain records the main machine's own facts for the machine list.
func (m *Manager) refreshMain(ctx context.Context) {
	mc, err := m.Store.GetMachine(ctx, api.MainMachine)
	if err != nil {
		return
	}
	info := mc.Info
	info.AgentVersion, info.OS, info.Arch = version.Version, hostOS, hostArch
	info.Engine = api.Engine{Driver: m.Local.Name()}
	if ei, err := m.Local.Info(ctx); err == nil {
		info.Engine.Available, info.Engine.Version, info.CPUs, info.MemoryTotalBytes = true, ei.Version, ei.CPUs, ei.MemoryTotalBytes
	} else {
		info.Engine.Message = err.Error()
	}
	if m.LocalHV != nil {
		info.VMs = m.LocalHV.Check(ctx).VMSupport
	}
	now := m.Now()
	mc.Info, mc.LastSeenAt = info, &now
	if err := m.Store.SaveMachineHealth(ctx, mc); err != nil && ctx.Err() == nil {
		m.Log.Warn("save the main machine's facts failed", "error", err)
	}
}

// check calls one agent's info and records what it found.
func (m *Manager) check(ctx context.Context, mc store.Machine) {
	r, err := m.client(ctx, mc)
	if err != nil {
		m.Log.Error("machine client", "machine", mc.Name, "error", err)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, orDefault(m.CheckTimeout, DefaultCheckTimeout))
	info, err := r.client.AgentInfo(cctx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	now := m.Now()
	if err == nil && info.MachineID != mc.ID {
		err = fmt.Errorf("the agent at %s is machine %s, not %s", mc.Address, info.MachineID, mc.ID)
	}
	if err != nil {
		m.Log.Debug("machine check failed", "machine", mc.Name, "error", err)
		if mc.State == api.MachineOnline && (mc.LastSeenAt == nil || now.Sub(*mc.LastSeenAt) >= m.offlineAfter()) {
			m.setState(ctx, mc, api.MachineOffline, now, fmt.Sprintf("%s stopped answering: %v", mc.Name, err))
		}
		return
	}
	mc.LastSeenAt = &now
	mc.Info = store.MachineInfo{OS: info.OS, Arch: info.Arch, AgentVersion: info.Version, Engine: info.Engine, CPUs: info.CPUs,
		MemoryTotalBytes: info.MemoryTotalBytes, VMs: info.VMs}
	if info.TunnelPublicKey != "" {
		mc.TunnelPublicKey = info.TunnelPublicKey
	}
	m.mu.Lock()
	r.info = info
	m.mu.Unlock()
	if mc.CertNotAfter != nil && mc.CertNotAfter.Sub(now) < agent.RenewBefore {
		m.renew(ctx, &mc, r)
	}
	if mc.State != api.MachineOnline {
		if err := m.sweep(ctx, mc, r); err != nil {
			m.Log.Warn("machine back, but not yet cleaned up", "machine", mc.Name, "error", err)
			_ = m.Store.SaveMachineHealth(ctx, mc)
			return
		}
		m.setState(ctx, mc, api.MachineOnline, now, mc.Name+" is online.")
		return
	}
	m.markOnline(mc.ID, true, mc.StateSince)
	if err := m.Store.SaveMachineHealth(ctx, mc); err != nil && ctx.Err() == nil {
		m.Log.Warn("save machine facts failed", "machine", mc.Name, "error", err)
	}
}

func (m *Manager) setState(ctx context.Context, mc store.Machine, state string, now time.Time, msg string) {
	mc.State, mc.StateSince = state, now
	if err := m.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.SaveMachineHealth(ctx, mc); err != nil {
			return err
		}
		return q.AddMachineEvent(ctx, mc.ID, state, msg, now)
	}); err != nil {
		m.Log.Error("save machine state failed", "machine", mc.Name, "error", err)
		return
	}
	m.markOnline(mc.ID, state == api.MachineOnline, now)
	m.Log.Info("machine "+state, "machine", mc.Name)
	if m.Changed != nil {
		m.Changed(mc.ID, state)
	}
}

func (m *Manager) markOnline(id string, online bool, since time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	r := m.remotes[id]
	if r == nil {
		r = &remote{}
		m.remotes[id] = r
	}
	if r.online != online {
		r.since = since
	}
	r.online = online
}

// Load reads the machines' last known states at start, so calls to machines that were online are tried at
// once; the first check decides.
func (m *Manager) Load(ctx context.Context) error {
	all, err := m.Store.ListMachines(ctx)
	if err != nil {
		return err
	}
	for _, mc := range all {
		if mc.ID == api.MainMachine || mc.State != api.MachineOnline {
			continue
		}
		if _, err := m.client(ctx, mc); err != nil {
			return err
		}
		m.markOnline(mc.ID, true, mc.StateSince)
	}
	return nil
}

var minorPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)`)

// SameMinor reports whether two versions share their major and minor numbers (MCH-10). Unnumbered builds
// match only themselves.
func SameMinor(a, b string) bool {
	ma, mb := minorPattern.FindStringSubmatch(a), minorPattern.FindStringSubmatch(b)
	if ma == nil || mb == nil {
		return a == b
	}
	return ma[1] == mb[1] && ma[2] == mb[2]
}

// Candidates describes every machine for placement (ADR-044): its state, labels, support, memory and what
// is reserved on it now.
func (m *Manager) Candidates(ctx context.Context) ([]domain.Candidate, error) {
	all, err := m.Store.ListMachines(ctx)
	if err != nil {
		return nil, err
	}
	resources, err := m.Store.AllResources(ctx)
	if err != nil {
		return nil, err
	}
	reserved := domain.ReservedByMachine(resources)
	out := make([]domain.Candidate, 0, len(all))
	for _, mc := range all {
		c := domain.Candidate{ID: mc.ID, Name: mc.Name, Main: mc.ID == api.MainMachine, Online: m.Online(mc.ID), Schedulable: mc.Schedulable,
			Labels: mc.Labels, VMs: mc.Info.VMs.Available, MemoryTotalMiB: int(mc.Info.MemoryTotalBytes >> 20), ReservedMiB: reserved[mc.ID],
			VersionOK: mc.Info.AgentVersion == "" || SameMinor(mc.Info.AgentVersion, version.Version)}
		if c.Main {
			c.VersionOK = true
		}
		out = append(out, c)
	}
	return out, nil
}

// Name is a machine's name, or its ID when it is gone.
func (m *Manager) Name(ctx context.Context, machineID string) string {
	if machineID == "" || machineID == api.MainMachine {
		return api.MainMachine
	}
	if mc, err := m.Store.GetMachine(ctx, machineID); err == nil {
		return mc.Name
	}
	return machineID
}

// Count is how many machines there are, joined or not.
func (m *Manager) Count(ctx context.Context) int {
	all, err := m.Store.ListMachines(ctx)
	if err != nil {
		return 1
	}
	return len(all)
}

// renew gives an agent a new certificate before its year is over (ADR-043). A failure is retried at the
// next check; the old certificate keeps working meanwhile.
func (m *Manager) renew(ctx context.Context, mc *store.Machine, r *remote) {
	ca, err := m.CA(ctx)
	if err != nil {
		return
	}
	var serial string
	var notAfter time.Time
	_, err = r.client.Renew(ctx, func(csr []byte) ([]byte, error) {
		c, s, na, err := ca.SignAgent(csr, mc.ID, m.Now())
		serial, notAfter = s, na
		return c, err
	})
	if err != nil {
		m.Log.Warn("renew the agent's certificate failed", "machine", mc.Name, "error", err)
		return
	}
	mc.CertSerial, mc.CertNotAfter = serial, &notAfter
	if err := m.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.SaveMachineHealth(ctx, *mc); err != nil {
			return err
		}
		return q.AddMachineEvent(ctx, mc.ID, "certificate_renewed", "Its certificate was renewed until "+notAfter.Format("2 Jan 2006")+".", m.Now())
	}); err != nil {
		m.Log.Error("save the renewed certificate failed", "machine", mc.Name, "error", err)
		return
	}
	if _, err := m.client(ctx, *mc); err != nil { // the next calls expect the new serial
		m.Log.Error("reconnect after renewal", "machine", mc.Name, "error", err)
	}
	m.Log.Info("agent certificate renewed", "machine", mc.Name)
}

// Members implements reconciler.Directory: every machine that joined, with its engine when online.
func (m *Manager) Members(ctx context.Context) ([]engine.MeshMember, error) {
	all, err := m.Store.ListMachines(ctx)
	if err != nil {
		return nil, err
	}
	var out []engine.MeshMember
	for _, mc := range all {
		if mc.State == api.MachineWaiting {
			continue
		}
		mem := engine.MeshMember{ID: mc.ID, Name: mc.Name, Main: mc.ID == api.MainMachine, PublicKey: mc.TunnelPublicKey, TunnelPort: mc.TunnelPort}
		if mem.Main {
			mem.TunnelPort = m.TunnelPort
			if m.TunnelAddress != "" {
				mem.Endpoint = net.JoinHostPort(m.TunnelAddress, strconv.Itoa(m.tunnelPort()))
			}
		} else if mc.Address != "" {
			mem.Endpoint = net.JoinHostPort(mc.Address, strconv.Itoa(mc.TunnelPort))
		}
		if mem.TunnelPort == 0 {
			mem.TunnelPort = api.TunnelPortDefault
		}
		if d, _, err := m.Engines(mc.ID); err == nil {
			mem.Online, mem.Driver = true, d
		}
		out = append(out, mem)
	}
	return out, nil
}

func (m *Manager) tunnelPort() int {
	if m.TunnelPort > 0 {
		return m.TunnelPort
	}
	return api.TunnelPortDefault
}

// RecordTunnelKey keeps the public key a machine's mesh reported (the main machine's comes this way;
// agents also report theirs with every health check).
func (m *Manager) RecordTunnelKey(ctx context.Context, machineID, key string) error {
	mc, err := m.Store.GetMachine(ctx, machineID)
	if err != nil || mc.TunnelPublicKey == key {
		return err
	}
	mc.TunnelPublicKey = key
	return m.Store.SaveMachineHealth(ctx, mc)
}

// sweep removes, on a machine that is back, the containers of everything no longer placed there: what
// started elsewhere while it was offline (ADR-046, FAIL-04). Volumes and data stay.
func (m *Manager) sweep(ctx context.Context, mc store.Machine, r *remote) error {
	on, err := m.Store.ResourcesOnMachine(ctx, mc.ID)
	if err != nil {
		return err
	}
	keep := make([]string, 0, len(on))
	for _, res := range on {
		keep = append(keep, res.ID)
	}
	removed, err := r.client.Sweep(ctx, keep)
	if err != nil {
		return err
	}
	if len(removed) > 0 {
		msg := fmt.Sprintf("Removed %d containers of what started elsewhere while it was offline.", len(removed))
		_ = m.Store.AddMachineEvent(ctx, mc.ID, "swept", msg, m.Now())
		m.Log.Info("swept a returning machine", "machine", mc.Name, "removed", len(removed))
	}
	return nil
}
