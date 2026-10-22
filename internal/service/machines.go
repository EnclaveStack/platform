package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/auth"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/machines"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/pkg/api"
)

// Machines adds, describes, changes and removes machines (L2-010 MCH-*, L3-002 §11.2).
type Machines struct {
	Store   *store.Store
	Manager *machines.Manager
	Queue   Queue
	Now     func() time.Time
	Log     *slog.Logger
	// Throttle limits join attempts per client address, like sign-ins.
	Throttle *auth.Throttle
	// ServerURL is where another computer reaches this control plane, for the join command; "" when only
	// this machine can (a loopback listen address).
	ServerURL func(ctx context.Context) string
	// Links reads each machine's tunnel links (XNET-06); nil before the mesh exists.
	Links func(ctx context.Context, machineID string) []api.MachineLink
}

func machineNotFound(name string) error {
	return api.NewError(api.CodeNotFound, "There's no machine named "+name+".")
}

func requireMachineAdmin(p Principal, what string) error {
	if !p.IsMachineAdmin() {
		return api.NewError(api.CodeForbidden, "Only machine admins "+what+".")
	}
	return nil
}

// List returns every machine, with resource counts over the projects the caller can see (L3-006 §10).
func (s *Machines) List(ctx context.Context, p Principal) (api.MachineList, error) {
	rows, err := s.Store.ListMachines(ctx)
	if err != nil {
		return api.MachineList{}, err
	}
	sum, err := s.summarizer(ctx, p)
	if err != nil {
		return api.MachineList{}, err
	}
	out := api.MachineList{Items: make([]api.MachineSummary, 0, len(rows))}
	for _, m := range rows {
		out.Items = append(out.Items, sum(m))
	}
	return out, nil
}

// Get returns one machine by name.
func (s *Machines) Get(ctx context.Context, p Principal, name string) (api.MachineSummary, error) {
	m, err := s.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return api.MachineSummary{}, machineNotFound(name)
	}
	if err != nil {
		return api.MachineSummary{}, err
	}
	sum, err := s.summarizer(ctx, p)
	if err != nil {
		return api.MachineSummary{}, err
	}
	return sum(m), nil
}

// summarizer reads what every summary needs once: resource counts the caller may see, and reservations.
func (s *Machines) summarizer(ctx context.Context, p Principal) (func(store.Machine) api.MachineSummary, error) {
	names, err := s.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	all := make([]string, len(names))
	for i, n := range names {
		all[i] = n.Name
	}
	counts, err := s.Store.MachineResourceCounts(ctx, p.VisibleProjects(all))
	if err != nil {
		return nil, err
	}
	resources, err := s.Store.AllResources(ctx)
	if err != nil {
		return nil, err
	}
	reserved := domain.ReservedByMachine(resources)
	title, _ := s.Store.GetSetting(ctx, "machine_name")
	return func(m store.Machine) api.MachineSummary {
		sum := api.MachineSummary{ID: m.ID, Name: m.Name, Title: m.Name, Main: m.ID == api.MainMachine, State: m.State, Since: m.StateSince,
			Labels: m.Labels, Schedulable: m.Schedulable, OS: m.Info.OS, Arch: m.Info.Arch, AgentVersion: m.Info.AgentVersion,
			Engine: m.Info.Engine, CPUs: m.Info.CPUs, MemoryTotalBytes: m.Info.MemoryTotalBytes, MemoryReservedBytes: int64(reserved[m.ID]) << 20,
			VMs: m.Info.VMs, LastSeenAt: m.LastSeenAt, Resources: counts[m.ID], Links: []api.MachineLink{}}
		if sum.Labels == nil {
			sum.Labels = map[string]string{}
		}
		if sum.Main && title != "" {
			sum.Title = title
		}
		if m.Address != "" {
			a := m.Address
			sum.Address = &a
		}
		if m.Info.AgentVersion != "" && !machines.SameMinor(m.Info.AgentVersion, version.Version) {
			sum.VersionMismatch = true
			sum.Message = fmt.Sprintf("Its agent runs EnclaveStack %s and this machine %s. Upgrade it to take new resources.", m.Info.AgentVersion, version.Version)
		}
		switch {
		case m.State == api.MachineWaiting && m.JoinExpiresAt != nil && s.Now().After(*m.JoinExpiresAt):
			sum.Message = "The join code expired. Make a new one."
		case m.State == api.MachineWaiting:
			sum.Message = "Waiting for its agent to join."
		case !m.Schedulable && sum.Message == "":
			sum.Message = "Not schedulable: nothing new is placed here."
		}
		if s.Links != nil && m.State == api.MachineOnline {
			sum.Links = s.Links(ctx, m.ID)
		}
		return sum
	}, nil
}

// Add makes a machine waiting for its agent, with a one-time join code (MCH-02, ADR-043).
func (s *Machines) Add(ctx context.Context, p Principal, req api.AddMachine) (api.AddedMachine, error) {
	if err := requireMachineAdmin(p, "add machines"); err != nil {
		return api.AddedMachine{}, err
	}
	req.Name = strings.TrimSpace(req.Name)
	if errs := api.ValidateAddMachine(req); len(errs) > 0 {
		return api.AddedMachine{}, api.Validation(errs)
	}
	if _, err := s.Manager.CA(ctx); err != nil { // made now, so the first join doesn't race to make it
		return api.AddedMachine{}, err
	}
	code, hash := newJoinCode()
	now := s.Now()
	exp := now.Add(api.JoinCodeTTL)
	m := store.Machine{ID: domain.NewID("mch"), Name: req.Name, State: api.MachineWaiting, StateSince: now, Labels: req.Labels, Schedulable: true,
		JoinCodeHash: hash, JoinExpiresAt: &exp, CreatedAt: now}
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.InsertMachine(ctx, m); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return &api.Error{Code: api.CodeNameTaken, Message: "A machine named " + req.Name + " already exists.",
					Details: []api.FieldError{{Field: "name", Code: api.FieldNotAllowed, Message: "A machine named " + req.Name + " already exists."}}}
			}
			return err
		}
		return q.AddMachineEvent(ctx, m.ID, "added", "Added by "+p.Actor()+".", now)
	})
	if err != nil {
		return api.AddedMachine{}, err
	}
	writeAudit(ctx, s.Store, now, s.Log, p, "machine_added", m.Name, "")
	return s.added(ctx, p, m, code)
}

// NewJoinCode replaces a waiting machine's code.
func (s *Machines) NewJoinCode(ctx context.Context, p Principal, name string) (api.AddedMachine, error) {
	if err := requireMachineAdmin(p, "make join codes"); err != nil {
		return api.AddedMachine{}, err
	}
	m, err := s.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return api.AddedMachine{}, machineNotFound(name)
	}
	if err != nil {
		return api.AddedMachine{}, err
	}
	if m.State != api.MachineWaiting {
		return api.AddedMachine{}, api.NewError(api.CodeActionNotSupported, m.Name+" has joined already. Remove it to join another computer under this name.")
	}
	code, hash := newJoinCode()
	exp := s.Now().Add(api.JoinCodeTTL)
	m.JoinCodeHash, m.JoinExpiresAt = hash, &exp
	if err := s.Store.SaveMachine(ctx, m); err != nil {
		return api.AddedMachine{}, err
	}
	return s.added(ctx, p, m, code)
}

func (s *Machines) added(ctx context.Context, p Principal, m store.Machine, code string) (api.AddedMachine, error) {
	sum, err := s.summarizer(ctx, p)
	if err != nil {
		return api.AddedMachine{}, err
	}
	server := ""
	if s.ServerURL != nil {
		server = s.ServerURL(ctx)
	}
	cmd := "enclave agent join " + server + " --code " + code
	if server == "" {
		cmd = "enclave agent join <this machine's address> --code " + code +
			"  # start the server with --listen 0.0.0.0:7070 (or --console-url) so the other computer can reach it"
	}
	return api.AddedMachine{Machine: sum(m), JoinCode: code, JoinCommand: cmd, ExpiresAt: *m.JoinExpiresAt}, nil
}

// Update changes a machine's labels or whether it takes new resources (MCH-07).
func (s *Machines) Update(ctx context.Context, p Principal, name string, req api.UpdateMachine) (api.MachineSummary, error) {
	if err := requireMachineAdmin(p, "change machines"); err != nil {
		return api.MachineSummary{}, err
	}
	if errs := api.ValidateLabels(req.Labels, "labels"); len(errs) > 0 {
		return api.MachineSummary{}, api.Validation(errs)
	}
	m, err := s.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return api.MachineSummary{}, machineNotFound(name)
	}
	if err != nil {
		return api.MachineSummary{}, err
	}
	var changes []string
	if req.Labels != nil {
		m.Labels = req.Labels
		changes = append(changes, "labels "+domain.FormatLabels(req.Labels))
	}
	if req.Schedulable != nil && *req.Schedulable != m.Schedulable {
		m.Schedulable = *req.Schedulable
		changes = append(changes, map[bool]string{true: "schedulable", false: "not schedulable"}[m.Schedulable])
	}
	now := s.Now()
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.SaveMachine(ctx, m); err != nil {
			return err
		}
		if len(changes) == 0 {
			return nil
		}
		return q.AddMachineEvent(ctx, m.ID, "updated", fmt.Sprintf("Changed by %s: %s.", p.Actor(), strings.Join(changes, ", ")), now)
	})
	if err != nil {
		return api.MachineSummary{}, err
	}
	return s.Get(ctx, p, name)
}

// Remove deletes a machine with nothing on it; force removes an offline one anyway, failing what ran there
// (MCH-09).
func (s *Machines) Remove(ctx context.Context, p Principal, name string, force bool) error {
	if err := requireMachineAdmin(p, "remove machines"); err != nil {
		return err
	}
	m, err := s.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return machineNotFound(name)
	}
	if err != nil {
		return err
	}
	if m.ID == api.MainMachine {
		return api.NewError(api.CodeActionNotSupported, "The main machine runs EnclaveStack itself and can't be removed.")
	}
	var failed []*domain.Resource
	now := s.Now()
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		on, err := q.ResourcesOnMachine(ctx, m.ID)
		if err != nil {
			return err
		}
		if len(on) > 0 && !force {
			e := &api.Error{Code: api.CodeMachineNotEmpty, Message: fmt.Sprintf("%s still runs %d resources. Move or delete them first.", m.Name, len(on))}
			for _, r := range on {
				e.Details = append(e.Details, api.FieldError{Field: qualifiedRef(r), Code: api.FieldNotAllowed, Message: "Runs on " + m.Name + "."})
			}
			return e
		}
		if len(on) > 0 && m.State == api.MachineOnline {
			return api.NewError(api.CodeMachineOnline, m.Name+" is online. Move what runs there first; force is only for a machine that is gone.")
		}
		for _, r := range on {
			// What ran there is gone with the machine: it is failed, off any machine, for the user to restore
			// or delete (MCH-09).
			r.Machine = ""
			if r.Phase != api.PhaseDeleting {
				if err := r.SetPhase(api.PhaseFailed, "The machine "+m.Name+" was removed. Restore it from a backup, or delete it.", now); err != nil {
					return err
				}
			}
			r.UpdatedAt = now
			if err := q.SaveResource(ctx, r); err != nil {
				return err
			}
			if err := q.AddEvent(ctx, r.ID, "machine_removed", "The machine "+m.Name+" was removed by "+p.Actor()+".", now); err != nil {
				return err
			}
			failed = append(failed, r)
		}
		return q.DeleteMachine(ctx, m.ID)
	})
	if err != nil {
		return err
	}
	s.Manager.Forget(m.ID)
	for _, r := range failed {
		s.Queue.Enqueue(r.ID)
	}
	writeAudit(ctx, s.Store, now, s.Log, p, "machine_removed", m.Name, map[bool]string{true: "forced", false: ""}[force])
	return nil
}

// Drain marks a machine not schedulable and moves everything on it that isn't pinned there (MCH-08).
func (s *Machines) Drain(ctx context.Context, p Principal, name string) (api.DrainResult, error) {
	if err := requireMachineAdmin(p, "drain machines"); err != nil {
		return api.DrainResult{}, err
	}
	m, err := s.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return api.DrainResult{}, machineNotFound(name)
	}
	if err != nil {
		return api.DrainResult{}, err
	}
	if m.ID == api.MainMachine {
		return api.DrainResult{}, api.NewError(api.CodeActionNotSupported, "The main machine keeps EnclaveStack's own services and can't be drained.")
	}
	out := api.DrainResult{Moving: []string{}, Pinned: []string{}}
	var queued []string
	now := s.Now()
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		m.Schedulable = false
		if err := q.SaveMachine(ctx, m); err != nil {
			return err
		}
		on, err := q.ResourcesOnMachine(ctx, m.ID)
		if err != nil {
			return err
		}
		for _, r := range on {
			spec, _, err := api.NormalizeSpec(r.Kind, r.Spec)
			if err != nil || r.Phase == api.PhaseDeleting {
				continue
			}
			if pl, _ := api.PlacementOf(spec); pl.Machine != "" || len(api.MountedVolumes(spec)) > 0 && r.Kind != api.KindVolume {
				out.Pinned = append(out.Pinned, qualifiedRef(r))
				continue
			}
			r.RequestedAction, r.Attempts, r.NextAttemptAt, r.UpdatedAt = domain.ActionMove, 0, nil, now
			if err := q.SaveResource(ctx, r); err != nil {
				return err
			}
			out.Moving = append(out.Moving, qualifiedRef(r))
			queued = append(queued, r.ID)
		}
		return q.AddMachineEvent(ctx, m.ID, "drain_started", fmt.Sprintf("Drained by %s: %d moving, %d pinned.", p.Actor(), len(out.Moving), len(out.Pinned)), now)
	})
	if err != nil {
		return api.DrainResult{}, err
	}
	for _, id := range queued {
		s.Queue.Enqueue(id)
	}
	return out, nil
}

// Events returns a machine's newest events.
func (s *Machines) Events(ctx context.Context, name string, limit int) (api.EventList, error) {
	m, err := s.Store.GetMachineByName(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return api.EventList{}, machineNotFound(name)
	}
	if err != nil {
		return api.EventList{}, err
	}
	ev, err := s.Store.ListMachineEvents(ctx, m.ID, limit)
	return api.EventList{Items: ev}, err
}

// Join signs a joining agent's certificate and records where it is (MCH-04, ADR-043). It is public: the
// code is the credential, used once, and throttled per client address.
func (s *Machines) Join(ctx context.Context, req api.JoinRequest, clientIP string) (api.JoinResponse, error) {
	key := "join:" + clientIP
	if s.Throttle != nil {
		if blocked, _ := s.Throttle.Blocked(key); blocked {
			return api.JoinResponse{}, api.NewError(api.CodeTooManyAttempts, "Too many join attempts. Wait a few minutes and try again.")
		}
	}
	req.Code, req.Advertise = strings.TrimSpace(req.Code), strings.TrimSpace(req.Advertise)
	if errs := api.ValidateJoin(req); len(errs) > 0 {
		return api.JoinResponse{}, api.Validation(errs)
	}
	invalid := api.NewError(api.CodeJoinCodeInvalid, "That join code is unknown, was used already or has expired. Make a new one on the main machine.")
	m, err := s.Store.GetMachineByJoinCode(ctx, hashCode(req.Code))
	if errors.Is(err, store.ErrNotFound) {
		if s.Throttle != nil {
			s.Throttle.Fail(key)
		}
		return api.JoinResponse{}, invalid
	}
	if err != nil {
		return api.JoinResponse{}, err
	}
	now := s.Now()
	if m.State != api.MachineWaiting || m.JoinExpiresAt == nil || now.After(*m.JoinExpiresAt) {
		if s.Throttle != nil {
			s.Throttle.Fail(key)
		}
		return api.JoinResponse{}, invalid
	}
	ca, err := s.Manager.CA(ctx)
	if err != nil {
		return api.JoinResponse{}, err
	}
	cert, serial, notAfter, err := ca.SignAgent([]byte(req.CSR), m.ID, now)
	if err != nil {
		return api.JoinResponse{}, api.Validation([]api.FieldError{{Field: "csr", Code: api.FieldInvalidFormat, Message: err.Error()}})
	}
	m.State, m.StateSince = api.MachineOffline, now // online once the first check reaches the agent
	m.JoinCodeHash, m.JoinExpiresAt = "", nil
	m.Address, m.AgentPort, m.TunnelPort = req.Advertise, req.AgentPort, req.TunnelPort
	m.CertSerial, m.CertNotAfter = serial, &notAfter
	m.Info = store.MachineInfo{OS: req.OS, Arch: req.Arch, AgentVersion: req.AgentVersion}
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.SaveMachine(ctx, m); err != nil {
			return err
		}
		return q.AddMachineEvent(ctx, m.ID, "joined", fmt.Sprintf("Joined from %s (agent at %s:%d).", clientIP, req.Advertise, req.AgentPort), now)
	})
	if err != nil {
		return api.JoinResponse{}, err
	}
	writeAudit(ctx, s.Store, now, s.Log, Principal{ClientIP: clientIP, Email: "agent " + m.Name}, "machine_joined", m.Name, req.Advertise)
	s.Log.Info("machine joined", "machine", m.Name, "address", req.Advertise)
	server := ""
	if s.ServerURL != nil {
		server = s.ServerURL(ctx)
	}
	return api.JoinResponse{MachineID: m.ID, Name: m.Name, Certificate: string(cert), CA: string(ca.PEM), Server: server}, nil
}

// qualifiedRef is "kind/name", with the project in front outside default.
func qualifiedRef(r *domain.Resource) string {
	if p := r.ProjectName(); p != api.DefaultProject {
		return p + "/" + r.Ref()
	}
	return r.Ref()
}

var joinEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// newJoinCode is 20 random bytes in base32, grouped for reading aloud, and its hash.
func newJoinCode() (code, hash string) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	raw := joinEncoding.EncodeToString(b)
	var groups []string
	for i := 0; i < len(raw); i += 8 {
		groups = append(groups, raw[i:min(i+8, len(raw))])
	}
	code = strings.Join(groups, "-")
	return code, hashCode(code)
}

func hashCode(code string) string {
	norm := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// ServerURL picks where other computers reach the control plane: the console URL, else the listen address
// when it isn't loopback, else nothing.
func ServerURL(consoleURL, listenURL string) string {
	if consoleURL != "" {
		return consoleURL
	}
	u, err := url.Parse(listenURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		if addr := firstLANAddress(); addr != "" {
			return u.Scheme + "://" + net.JoinHostPort(addr, u.Port())
		}
		return ""
	}
	return listenURL
}

// firstLANAddress is this computer's first private IPv4 address, for a server listening on every interface.
func firstLANAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	var found []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
			found = append(found, n.IP.String())
		}
	}
	slices.Sort(found)
	if len(found) == 0 {
		return ""
	}
	return found[0]
}

// checkMachine refuses a spec naming a machine that doesn't exist (L3-002 §11.3).
func checkMachine(ctx context.Context, q *store.Queries, spec any) error {
	p, ok := api.PlacementOf(spec)
	if !ok || p.Machine == "" {
		return nil
	}
	if _, err := q.GetMachineByName(ctx, p.Machine); errors.Is(err, store.ErrNotFound) {
		return api.Validation([]api.FieldError{{Field: "spec.machine", Code: api.FieldNotFound, Message: "There's no machine named " + p.Machine + "."}})
	} else if err != nil {
		return err
	}
	return nil
}

// checkMove refuses a move that can't happen (L3-002 §11.4): to a machine that is offline, not schedulable
// or of another version; of data whose machine is offline (FAIL-03); of something that uses a volume without
// it (PLC-04).
func (s *Resources) checkMove(ctx context.Context, q *store.Queries, r *domain.Resource, before, after any) error {
	pb, ok := api.PlacementOf(before)
	pa, _ := api.PlacementOf(after)
	if !ok || r.Machine == "" || pa.Machine == pb.Machine && labelsEqual(pa.MachineLabels, pb.MachineLabels) {
		return nil
	}
	if pa.Machine != "" {
		to, err := q.GetMachineByName(ctx, pa.Machine)
		if err != nil {
			return err // checkMachine reported an unknown one
		}
		field := func(msg string) error {
			return api.Validation([]api.FieldError{{Field: "spec.machine", Code: api.FieldNotAllowed, Message: msg}})
		}
		switch {
		case to.ID == r.Machine:
			return nil
		case s.MachineOnline != nil && !s.MachineOnline(to.ID):
			return field(to.Name + " is offline. Move it once it is back.")
		case !to.Schedulable:
			return field(to.Name + " isn't schedulable: it takes nothing new.")
		case to.Info.AgentVersion != "" && !machines.SameMinor(to.Info.AgentVersion, version.Version):
			return field(to.Name + " runs another version of EnclaveStack. Upgrade it first.")
		}
	}
	if api.IsDataKind(r.Kind) && s.MachineOnline != nil && !s.MachineOnline(r.MachineID()) {
		from, _ := q.GetMachine(ctx, r.MachineID())
		return api.NewError(api.CodeMachineOffline, fmt.Sprintf("%s is offline, so the data of %s can't be read. Restore a backup to a new %s instead.", from.Name, r.Name, r.Kind))
	}
	if mounts := api.MountedVolumes(after); len(mounts) > 0 {
		return api.Validation([]api.FieldError{{Field: "spec.machine", Code: api.FieldNotAllowed,
			Message: fmt.Sprintf("%s uses the volume %s, and runs where it is. Move the volume, and %s moves with it.", r.Name, mounts[0], r.Name)}})
	}
	return nil
}

func labelsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
