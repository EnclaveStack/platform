package httpapi_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/server"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Machines and placement (L2-010 MCH-*, PLC-*; ADR-042 – ADR-044), with real agents over mutual TLS and fake
// engines behind them.

func viewerClient(t *testing.T, env *testenv.Env, admin *client.Client) *client.Client {
	t.Helper()
	tok, err := admin.CreateToken(context.Background(), api.CreateTokenRequest{Name: "viewer", Role: api.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	return client.New(env.URL, tok.Secret)
}

func TestMachinesJoinGoOfflineComeBackAndAreRemoved(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()

	list, err := admin.ListMachines(ctx)
	if err != nil || len(list) != 1 || list[0].Name != testenv.MainName() || !list[0].Main || list[0].Title != "Test machine" || list[0].State != api.MachineOnline {
		t.Fatalf("before any machine: %+v %v", list, err)
	}

	rack := env.AddAgent(t, admin, "rack-1", map[string]string{"disk": "ssd"})
	m, err := admin.GetMachineByName(ctx, "rack-1")
	if err != nil || m.CPUs != 8 || m.MemoryTotalBytes != 16<<30 || m.Labels["disk"] != "ssd" || m.Address == nil || *m.Address != "127.0.0.1" || m.Engine.Version != "fake-1.0" {
		t.Fatalf("rack-1: %+v %v", m, err)
	}
	events, _ := admin.MachineEvents(ctx, "rack-1")
	if !hasEvent(events, "joined") || !hasEvent(events, "online") || !hasEvent(events, "added") {
		t.Errorf("events: %+v", events)
	}

	// A code works once and only for an hour (MCH-04).
	added, err := admin.AddMachine(ctx, api.AddMachine{Name: "rack-2"})
	if err != nil || !strings.Contains(added.JoinCommand, added.JoinCode) || added.Machine.State != api.MachineWaiting {
		t.Fatalf("add rack-2: %+v %v", added, err)
	}
	dir := t.TempDir()
	if _, err := agent.Join(ctx, agent.JoinOptions{Server: env.URL, Code: added.JoinCode, Advertise: "127.0.0.1", Listen: "127.0.0.1:1", StateDir: dir + "/a"}); err != nil {
		t.Fatal(err)
	}
	_, err = agent.Join(ctx, agent.JoinOptions{Server: env.URL, Code: added.JoinCode, Advertise: "127.0.0.1", Listen: "127.0.0.1:1", StateDir: dir + "/b"})
	mustCode(t, err, api.CodeJoinCodeInvalid, "a code used twice")
	_, err = agent.Join(ctx, agent.JoinOptions{Server: env.URL, Code: "NOPE-NOPE", Advertise: "127.0.0.1", Listen: "127.0.0.1:1", StateDir: dir + "/c"})
	mustCode(t, err, api.CodeJoinCodeInvalid, "an unknown code")
	_, err = admin.NewJoinCode(ctx, "rack-2")
	mustCode(t, err, api.CodeActionNotSupported, "a new code for a machine that joined")

	// The agent stops answering: offline after the grace period, with an event; back online on its return (MCH-06).
	rack.Stop()
	env.WaitMachine(t, admin, "rack-1", api.MachineOffline)
	rack.Start()
	env.WaitMachine(t, admin, "rack-1", api.MachineOnline)
	if events, _ = admin.MachineEvents(ctx, "rack-1"); !hasEvent(events, "offline") {
		t.Errorf("no offline event: %+v", events)
	}

	// Only machine admins add, change and remove machines; anyone signed in reads them (MCH-11).
	viewer := viewerClient(t, env, admin)
	if _, err := viewer.ListMachines(ctx); err != nil {
		t.Errorf("a viewer lists machines: %v", err)
	}
	_, err = viewer.AddMachine(ctx, api.AddMachine{Name: "sneaky"})
	mustCode(t, err, api.CodeForbidden, "a viewer adding a machine")
	err = viewer.RemoveMachine(ctx, "rack-1", false)
	mustCode(t, err, api.CodeForbidden, "a viewer removing a machine")

	// Not schedulable (MCH-07).
	m, err = admin.UpdateMachineByName(ctx, "rack-1", api.UpdateMachine{Schedulable: ptr(false)})
	if err != nil || m.Schedulable || !strings.Contains(m.Message, "Not schedulable") {
		t.Fatalf("cordon: %+v %v", m, err)
	}

	// Removing: never the main machine; refused while something runs there (MCH-09).
	err = admin.RemoveMachine(ctx, testenv.MainName(), false)
	mustCode(t, err, api.CodeActionNotSupported, "removing the main machine")
	if _, err := admin.UpdateMachineByName(ctx, "rack-1", api.UpdateMachine{Schedulable: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	db, err := admin.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{Placed: api.Placed{Machine: "rack-1"}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, db.ID)
	err = admin.RemoveMachine(ctx, "rack-1", false)
	mustCode(t, err, api.CodeMachineNotEmpty, "removing a machine with a database on it")
	err = admin.RemoveMachine(ctx, "rack-1", true)
	mustCode(t, err, api.CodeMachineOnline, "forcing the removal of an online machine")
	if err := admin.RemoveMachine(ctx, "rack-2", false); err != nil {
		t.Errorf("removing an empty machine: %v", err)
	}

	// A machine that is gone for good: force fails what ran there, with the reason.
	rack.Stop()
	env.WaitMachine(t, admin, "rack-1", api.MachineOffline)
	if err := admin.RemoveMachine(ctx, "rack-1", true); err != nil {
		t.Fatalf("force: %v", err)
	}
	got, _ := admin.GetResource(ctx, db.ID)
	if got.Status.Phase != api.PhaseFailed || !strings.Contains(got.Status.Message, "rack-1 was removed") {
		t.Errorf("the database after its machine was removed: %+v", got.Status)
	}
	if _, err := admin.GetMachineByName(ctx, "rack-1"); client.Code(err) != api.CodeNotFound {
		t.Errorf("rack-1 still listed: %v", err)
	}
}

func TestPlacementRunsEachResourceOnItsMachine(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()
	rack := env.AddAgent(t, admin, "rack-1", map[string]string{"disk": "ssd"})
	rack.Driver.SetMemory(32 << 30)
	waitMemory(t, admin, "rack-1", 32<<30)

	where := func(r api.Resource) string { s, _ := r.Outputs["machine"].(string); return s }

	// Chosen by free memory (PLC-02): rack-1 has more.
	db, err := admin.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{})
	if err != nil {
		t.Fatal(err)
	}
	db = testenv.WaitReady(t, admin, db.ID)
	if where(db) != "rack-1" || rack.Driver.EnsureCalls("orders") == 0 || env.Fake.EnsureCalls("orders") != 0 {
		t.Fatalf("orders on %q (rack-1 calls %d, main calls %d)", where(db), rack.Driver.EnsureCalls("orders"), env.Fake.EnsureCalls("orders"))
	}
	if ev, _ := admin.Events(ctx, db.ID, 20); !hasEvent(ev, "placed") {
		t.Errorf("no placed event: %+v", ev)
	}

	// Pinned to a machine, or to labels.
	web, err := admin.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80, Placed: api.Placed{Machine: testenv.MainName()}})
	if err != nil {
		t.Fatal(err)
	}
	if web = testenv.WaitReady(t, admin, web.ID); where(web) != testenv.MainName() || env.Fake.EnsureCalls("web") == 0 {
		t.Errorf("web on %q", where(web))
	}
	cache, err := admin.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{Placed: api.Placed{MachineLabels: map[string]string{"disk": "ssd"}}})
	if err != nil {
		t.Fatal(err)
	}
	if cache = testenv.WaitReady(t, admin, cache.ID); where(cache) != "rack-1" {
		t.Errorf("sessions on %q", where(cache))
	}

	// Nothing fits: it waits, saying why (PLC-02).
	none, err := admin.CreateResource(ctx, api.KindCache, "nowhere", api.CacheSpec{Placed: api.Placed{MachineLabels: map[string]string{"disk": "nvme"}}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "the reason it waits", func() bool {
		r, _ := admin.GetResource(ctx, none.ID)
		return r.Status.Phase == api.PhaseProvisioning && strings.Contains(r.Status.Message, "No machine has the labels disk=nvme")
	})

	// An unknown machine is refused (L3-002 §11.3).
	_, err = admin.CreateResource(ctx, api.KindApp, "lost", api.AppSpec{Image: "nginx", Placed: api.Placed{Machine: "nowhere"}})
	mustCode(t, err, api.CodeValidationFailed, "an unknown machine")

	// What runs where, per machine.
	on, err := admin.ListResources(ctx, client.ListOptions{Machine: "rack-1"})
	if err != nil || len(on.Items) != 2 {
		t.Errorf("on rack-1: %d %v", len(on.Items), err)
	}
	m, _ := admin.GetMachineByName(ctx, "rack-1")
	if m.Resources != 2 || m.MemoryReservedBytes == 0 {
		t.Errorf("rack-1's counts: %d resources, %d reserved", m.Resources, m.MemoryReservedBytes)
	}

	// Logs and deletes reach the machine too.
	if _, err := admin.Logs(ctx, db.ID, 10); err != nil {
		t.Errorf("logs from rack-1: %v", err)
	}
	if _, err := admin.DeleteResource(ctx, db.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "orders deleted on rack-1", func() bool {
		_, err := admin.GetResource(ctx, db.ID)
		return client.Code(err) == api.CodeNotFound
	})

	// Its machine offline: a resource pinned there waits with the machine in its message (FAIL-01).
	rack.Stop()
	env.WaitMachine(t, admin, "rack-1", api.MachineOffline)
	late, err := admin.CreateResource(ctx, api.KindCache, "late", api.CacheSpec{Placed: api.Placed{Machine: "rack-1"}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 5*time.Second, "waiting for rack-1", func() bool {
		r, _ := admin.GetResource(ctx, late.ID)
		return strings.Contains(r.Status.Message, "rack-1 is offline")
	})
	// Back online: what waited goes ahead at once, not at its next try 30 s later.
	rack.Start()
	env.WaitMachine(t, admin, "rack-1", api.MachineOnline)
	start := time.Now()
	testenv.WaitReady(t, admin, late.ID)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("late waited %s after rack-1 came back", waited)
	}
}

func hasEvent(events []api.Event, typ string) bool {
	for _, e := range events {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// Networks across machines (L2-010 XNET-*, ADR-045): what each machine is told.
func TestTheMeshGivesEachMachineItsPeersPoolsAndStandIns(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.TunnelAddress, c.MeshEvery = "127.0.0.1", 100*time.Millisecond }))
	admin := env.Setup(t)
	ctx := context.Background()

	// One machine: no mesh at all (PLC-08).
	web, err := admin.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80, Placed: api.Placed{Machine: testenv.MainName()}})
	if err != nil {
		t.Fatal(err)
	}
	web = testenv.WaitReady(t, admin, web.ID)
	if m := env.Fake.Mesh(); m != nil && m.Enabled {
		t.Fatalf("a mesh with one machine: %+v", m)
	}

	rack := env.AddAgent(t, admin, "rack-1", nil)
	testenv.Eventually(t, 5*time.Second, "both machines meshed", func() bool {
		a, b := env.Fake.Mesh(), rack.Driver.Mesh()
		return a != nil && a.Enabled && b != nil && b.Enabled && len(a.Peers) == 1 && len(b.Peers) == 1 && b.Peers[0].PublicKey != ""
	})
	mainCfg, rackCfg := env.Fake.Mesh(), rack.Driver.Mesh()
	if rackCfg.DefaultRange != mainCfg.DefaultRange || rackCfg.Peers[0].Endpoint != "127.0.0.1:51820" {
		t.Errorf("rack-1's mesh: range %q (main %q), peer at %q", rackCfg.DefaultRange, mainCfg.DefaultRange, rackCfg.Peers[0].Endpoint)
	}
	// What ran before keeps its address, in the main machine's pool; the pools never share one (XNET-02).
	webIP, _ := web.Outputs["privateIp"].(string)
	if !slices.Contains(mainCfg.Pools[engine.NetworkName], webIP) {
		t.Errorf("web's %s isn't in the main machine's pool %v", webIP, mainCfg.Pools[engine.NetworkName])
	}
	for _, a := range rackCfg.Pools[engine.NetworkName] {
		if slices.Contains(mainCfg.Pools[engine.NetworkName], a) {
			t.Errorf("%s is in both pools", a)
		}
	}

	// A database on rack-1: its address comes from rack-1's pool, so it isn't web's, though both fakes pick
	// the lowest free address; the main machine gets a stand-in for it and a peer route.
	db, err := admin.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{Placed: api.Placed{Machine: "rack-1"}})
	if err != nil {
		t.Fatal(err)
	}
	db = testenv.WaitReady(t, admin, db.ID)
	dbIP, _ := db.Outputs["privateIp"].(string)
	if dbIP == "" || dbIP == webIP || !slices.Contains(rack.Driver.Mesh().Pools[engine.NetworkName], dbIP) {
		t.Fatalf("orders at %q, web at %q", dbIP, webIP)
	}
	testenv.Eventually(t, 5*time.Second, "a stand-in for orders on the main machine", func() bool {
		m := env.Fake.Mesh()
		for _, s := range m.StandIns {
			if s.Address == dbIP && slices.Contains(s.Aliases, "orders.database.internal") && s.Machine == rack.ID {
				return slices.Contains(m.Peers[0].Addresses, dbIP)
			}
		}
		return false
	})
	// The link is connected one mesh pass later than the stand-ins: the first pass learns the main machine's
	// tunnel key, and only the next one hands it to rack-1, whose handshake is then recognised (XNET-06).
	testenv.Eventually(t, 10*time.Second, "rack-1's link to the main machine connected", func() bool {
		mc, _ := admin.GetMachineByName(ctx, "rack-1")
		return len(mc.Links) == 1 && mc.Links[0].Connected && mc.Links[0].Machine == testenv.MainName()
	})

	// rack-1 offline: the main machine drops its peer and stand-ins.
	rack.Stop()
	env.WaitMachine(t, admin, "rack-1", api.MachineOffline)
	testenv.Eventually(t, 5*time.Second, "rack-1 out of the main machine's mesh", func() bool {
		m := env.Fake.Mesh()
		return m != nil && len(m.Peers) == 0 && len(m.StandIns) == 0
	})
}

// A machine goes away (L2-010 FAIL-*, ADR-046): its app starts elsewhere, its database waits, an alert
// fires, and when it is back the app's old containers are swept and the database carries on.
func TestAnOfflineMachinesAppsStartElsewhereAndItsDataWaits(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) {
		c.TunnelAddress, c.MeshEvery, c.RescheduleAfter = "127.0.0.1", 100*time.Millisecond, 300*time.Millisecond
	}))
	admin := env.Setup(t)
	ctx := context.Background()
	rack := env.AddAgent(t, admin, "rack-1", nil)
	where := func(id string) string {
		r, _ := admin.GetResource(ctx, id)
		s, _ := r.Outputs["machine"].(string)
		return s
	}

	// Pinning would keep it there; rack-1 has more memory, so it is chosen and may move.
	rack.Driver.SetMemory(64 << 30)
	waitMemory(t, admin, "rack-1", 64<<30)
	web, err := admin.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, web.ID)
	db, err := admin.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, db.ID)
	if where(web.ID) != "rack-1" || where(db.ID) != "rack-1" {
		t.Fatalf("web on %q, orders on %q", where(web.ID), where(db.ID))
	}
	alert, err := admin.CreateResource(ctx, api.KindChannel, "hook", api.ChannelSpec{Type: api.ChannelWebhook, Webhook: &api.WebhookTarget{URL: "http://127.0.0.1:1/hook"}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, alert.ID)
	al, err := admin.CreateResource(ctx, api.KindAlert, "rack-down", api.AlertSpec{Channels: []string{"hook"},
		Condition: api.AlertCondition{Type: api.ConditionMachineOffline, Resources: []string{"machine/rack-1"}, ForMinutes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, al.ID)

	rack.Stop()
	env.WaitMachine(t, admin, "rack-1", api.MachineOffline)
	// The app starts on the main machine; the database waits, saying why (FAIL-01 – FAIL-03).
	testenv.Eventually(t, 10*time.Second, "web on the main machine", func() bool {
		r, _ := admin.GetResource(ctx, web.ID)
		m, _ := r.Outputs["machine"].(string)
		return m == testenv.MainName() && r.Status.Phase == api.PhaseReady
	})
	if env.Fake.EnsureCalls("web") == 0 {
		t.Error("web never ran on the main machine")
	}
	if ev, _ := admin.Events(ctx, web.ID, 20); !hasEvent(ev, "rescheduled") {
		t.Errorf("no rescheduled event: %+v", ev)
	}
	if where(db.ID) != "rack-1" {
		t.Errorf("the database moved: %q", where(db.ID))
	}
	// The alert fires once its minute is up (MCH-06); the monitor runs on a clock the test moves.
	env.Monitor.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if err := env.Monitor.EvaluateOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if a, _ := admin.GetResource(ctx, al.ID); a.Outputs["state"] != api.AlertFiring {
		t.Errorf("the alert: %v", a.Outputs)
	}

	// Back: web's old containers are swept from rack-1; the database carries on with its data (FAIL-04).
	if !rack.Driver.Running(web.ID) {
		t.Fatal("the fake lost web's runtime before the machine came back")
	}
	rack.Start()
	env.WaitMachine(t, admin, "rack-1", api.MachineOnline)
	if rack.Driver.Running(web.ID) {
		t.Error("web still runs on rack-1 after it came back: it runs twice")
	}
	if !rack.Driver.Running(db.ID) {
		t.Error("the database's runtime was swept")
	}
	if ev, _ := admin.MachineEvents(ctx, "rack-1"); !hasEvent(ev, "swept") {
		t.Errorf("no swept event: %+v", ev)
	}
	if where(web.ID) != testenv.MainName() {
		t.Errorf("web went back: %q", where(web.ID))
	}
}

// Moving (L2-010 MOVE-*, MCH-08; ADR-047): copy, then switch, for every kind with data; refusals; a move
// that fails leaves things where they were; a drain.
func TestResourcesMoveWithTheirData(t *testing.T) {
	env := testenv.Start(t, testenv.WithConfig(func(c *server.Config) { c.TunnelAddress, c.MeshEvery = "127.0.0.1", 100*time.Millisecond }))
	admin := env.Setup(t)
	ctx := context.Background()
	rack := env.AddAgent(t, admin, "rack-1", nil)
	where := func(id string) string {
		r, _ := admin.GetResource(ctx, id)
		s, _ := r.Outputs["machine"].(string)
		return s
	}
	move := func(id, to string) api.Resource {
		t.Helper()
		if _, err := admin.UpdateResource(ctx, id, map[string]any{"machine": to}, nil); err != nil {
			t.Fatalf("move %s to %s: %v", id, to, err)
		}
		testenv.Eventually(t, 10*time.Second, id+" on "+to, func() bool {
			r, _ := admin.GetResource(ctx, id)
			m, _ := r.Outputs["machine"].(string)
			ev, _ := admin.Events(ctx, id, 50)
			return m == to && r.Status.Phase == api.PhaseReady && hasEvent(ev, "moved") // the old copy goes just after ready
		})
		r, _ := admin.GetResource(ctx, id)
		return r
	}

	// A database: its data and its address go with it (MOVE-02).
	db, err := admin.CreateResource(ctx, api.KindDatabase, "orders", api.DatabaseSpec{Placed: api.Placed{Machine: "rack-1"}})
	if err != nil {
		t.Fatal(err)
	}
	db = testenv.WaitReady(t, admin, db.ID)
	ip := db.Outputs["privateIp"]
	rack.Driver.SetData(db.ID, "rows: 3")
	db = move(db.ID, testenv.MainName())
	if env.Fake.Data(db.ID) != "rows: 3" || db.Outputs["privateIp"] != ip {
		t.Errorf("after the move: data %q, address %v (was %v)", env.Fake.Data(db.ID), db.Outputs["privateIp"], ip)
	}
	if rack.Driver.Running(db.ID) {
		t.Error("the old copy still runs on rack-1")
	}
	ev, _ := admin.Events(ctx, db.ID, 20)
	if !hasEvent(ev, "move_started") || !hasEvent(ev, "moved") || !strings.Contains(eventMessage(ev, "moved"), "unavailable for") {
		t.Errorf("events: %+v", ev)
	}

	// An app: a deploy on the new machine, then the old one goes (MOVE-01).
	web, err := admin.CreateResource(ctx, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80, Placed: api.Placed{Machine: testenv.MainName()}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, web.ID)
	move(web.ID, "rack-1")
	if !rack.Driver.Running(web.ID) || env.Fake.Running(web.ID) {
		t.Errorf("web: on rack-1 %v, on main %v", rack.Driver.Running(web.ID), env.Fake.Running(web.ID))
	}

	// A volume moves with its files, and the app that mounts it follows (MOVE-03).
	vol, err := admin.CreateResource(ctx, api.KindVolume, "uploads", api.VolumeSpec{Type: api.VolumeFilesystem, Placed: api.Placed{Machine: testenv.MainName()}})
	if err != nil {
		t.Fatal(err)
	}
	vol = testenv.WaitReady(t, admin, vol.ID)
	blog, err := admin.CreateResource(ctx, api.KindApp, "blog", api.AppSpec{Image: "nginx", Port: 80, Volumes: []api.VolumeMount{{Volume: "uploads", Path: "/data"}}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, blog.ID)
	if where(blog.ID) != testenv.MainName() {
		t.Fatalf("blog didn't follow its volume: %q", where(blog.ID))
	}
	env.Fake.WriteFile(vol.ID, "a.txt", "hello")
	_, err = admin.UpdateResource(ctx, blog.ID, map[string]any{"machine": "rack-1"}, nil)
	mustCode(t, err, api.CodeValidationFailed, "moving an app without its volume")
	move(vol.ID, "rack-1")
	if rack.Driver.Files(vol.ID)["a.txt"] != "hello" {
		t.Errorf("the volume's files on rack-1: %v", rack.Driver.Files(vol.ID))
	}
	testenv.Eventually(t, 10*time.Second, "blog on rack-1 with its volume", func() bool {
		r, _ := admin.GetResource(ctx, blog.ID)
		m, _ := r.Outputs["machine"].(string)
		return m == "rack-1" && r.Status.Phase == api.PhaseReady && rack.Driver.Running(blog.ID)
	})

	// A VM: shut down, its root disk copied, started there (MOVE-04).
	vm, err := admin.CreateResource(ctx, api.KindVM, "box", api.VMSpec{Image: "debian-12", SSHKeys: []string{testSSHKey}, Placed: api.Placed{Machine: testenv.MainName()}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, vm.ID)
	env.FakeHypervisor.WriteRoot(vm.ID, "my files")
	move(vm.ID, "rack-1")
	if v := rack.Hypervisor.VM(vm.ID); v == nil || v.Root != "my files" || !v.Running {
		t.Errorf("the VM on rack-1: %+v", v)
	}
	if env.FakeHypervisor.VM(vm.ID) != nil {
		t.Error("the VM is still on the main machine")
	}

	// Refusals (L3-002 §11.4).
	if _, err := admin.UpdateMachineByName(ctx, testenv.MainName(), api.UpdateMachine{Schedulable: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	_, err = admin.UpdateResource(ctx, web.ID, map[string]any{"machine": testenv.MainName()}, nil)
	mustCode(t, err, api.CodeValidationFailed, "a move to a machine that isn't schedulable")
	if _, err := admin.UpdateMachineByName(ctx, testenv.MainName(), api.UpdateMachine{Schedulable: ptr(true)}); err != nil {
		t.Fatal(err)
	}

	// A move that fails leaves it where it was, running (MOVE-06): rack-1's engine is down for the copy.
	cache, err := admin.CreateResource(ctx, api.KindCache, "sessions", api.CacheSpec{Placed: api.Placed{Machine: testenv.MainName()}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, cache.ID)
	rack.Driver.SetAvailable(false)
	if _, err := admin.UpdateResource(ctx, cache.ID, map[string]any{"machine": "rack-1"}, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "the failed move", func() bool {
		ev, _ := admin.Events(ctx, cache.ID, 20)
		return hasEvent(ev, "move_failed")
	})
	if !env.Fake.Running(cache.ID) || where(cache.ID) != testenv.MainName() {
		t.Errorf("after a failed move: running on main %v, machine %q", env.Fake.Running(cache.ID), where(cache.ID))
	}
	rack.Driver.SetAvailable(true)
	testenv.Eventually(t, 30*time.Second, "the move once rack-1 works", func() bool { return where(cache.ID) == "rack-1" })

	// Data can't leave a machine that is offline (FAIL-03).
	rack.Stop()
	env.WaitMachine(t, admin, "rack-1", api.MachineOffline)
	_, err = admin.UpdateResource(ctx, cache.ID, map[string]any{"machine": testenv.MainName()}, nil)
	mustCode(t, err, api.CodeMachineOffline, "moving data off an offline machine")
	rack.Start()
	env.WaitMachine(t, admin, "rack-1", api.MachineOnline)

	// A drain moves what isn't pinned; pinned resources are listed (MCH-08).
	rack.Driver.SetMemory(64 << 30)
	waitMemory(t, admin, "rack-1", 64<<30)
	free, err := admin.CreateResource(ctx, api.KindApp, "free", api.AppSpec{Image: "nginx", Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, free.ID)
	if where(free.ID) != "rack-1" {
		t.Fatalf("free on %q", where(free.ID))
	}
	dr, err := admin.DrainMachine(ctx, "rack-1")
	if err != nil || !slices.Contains(dr.Moving, "app/free") || !slices.Contains(dr.Pinned, "app/web") {
		t.Fatalf("drain: %+v %v", dr, err)
	}
	testenv.Eventually(t, 10*time.Second, "free off rack-1", func() bool { return where(free.ID) == testenv.MainName() })
	if m, _ := admin.GetMachineByName(ctx, "rack-1"); m.Schedulable {
		t.Error("a drained machine is still schedulable")
	}
}

func eventMessage(events []api.Event, typ string) string {
	for _, e := range events {
		if e.Type == typ {
			return e.Message
		}
	}
	return ""
}

const testSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXTqZbAPryM1c8TBIUjg6rtRL1Dfr2uR7E954v/Icd4 test@enclave"

// waitMemory waits until a machine's health checks have recorded its memory (a check that read it before
// the change may still be saving the old figure).
func waitMemory(t *testing.T, c *client.Client, name string, bytes int64) {
	t.Helper()
	testenv.Eventually(t, 5*time.Second, name+"'s memory", func() bool {
		m, err := c.GetMachineByName(context.Background(), name)
		return err == nil && m.MemoryTotalBytes == bytes
	})
	time.Sleep(250 * time.Millisecond) // and stays: one more round of checks
	if m, _ := c.GetMachineByName(context.Background(), name); m.MemoryTotalBytes != bytes {
		t.Fatalf("%s's memory went back to %d", name, m.MemoryTotalBytes)
	}
}

func TestTheMainMachineIsRenamedByItsTitleAndNothingElseChanges(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()
	rack := env.AddAgent(t, admin, "rack-1", nil)
	_ = rack

	before, err := admin.GetMachineByName(ctx, testenv.MainName())
	if err != nil || before.Title != "Test machine" {
		t.Fatalf("before: %+v %v", before, err)
	}
	got, err := admin.UpdateMachineByName(ctx, testenv.MainName(), api.UpdateMachine{Title: ptr("  Home lab  ")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Home lab" || got.ID != before.ID || got.Name != before.Name {
		t.Errorf("after a rename: title %q, id %q, name %q (was %q, %q)", got.Title, got.ID, got.Name, before.ID, before.Name)
	}
	if m, _ := admin.Machine(ctx); m.Name != "Home lab" {
		t.Errorf("the machine's own name is %q, want the new title", m.Name)
	}
	if s, _ := admin.Session(ctx); s.MachineName != "Home lab" {
		t.Errorf("the session says %q", s.MachineName)
	}

	_, err = admin.UpdateMachineByName(ctx, testenv.MainName(), api.UpdateMachine{Title: ptr("   ")})
	mustCode(t, err, api.CodeValidationFailed, "a blank display name")
	_, err = admin.UpdateMachineByName(ctx, "rack-1", api.UpdateMachine{Title: ptr("Rack")})
	mustCode(t, err, api.CodeValidationFailed, "a display name on a machine that has none")

	// Anyone but a machine admin is refused.
	viewer, _ := env.Token(t, api.ScopeRead)
	_, err = viewer.UpdateMachineByName(ctx, testenv.MainName(), api.UpdateMachine{Title: ptr("Mine")})
	mustCode(t, err, api.CodeForbidden, "a viewer renaming the machine")
}

func TestSetupSuggestsTheComputersName(t *testing.T) {
	env := testenv.Start(t)
	st, err := client.New(env.URL, "").GetSetup(context.Background())
	if err != nil || !st.Required || strings.TrimSpace(st.SuggestedMachineName) == "" {
		t.Errorf("setup status %+v, %v", st, err)
	}
}
