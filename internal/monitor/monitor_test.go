package monitor_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/monitor"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/internal/webhook"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

var ctx = context.Background()

// receiver records requests and answers with status (200 by default).
type receiver struct {
	mu     sync.Mutex
	status int
	reqs   []recorded
	srv    *httptest.Server
}

type recorded struct {
	path    string
	headers http.Header
	body    []byte
}

func newReceiver(t *testing.T) *receiver {
	rc := &receiver{status: 200}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rc.mu.Lock()
		rc.reqs = append(rc.reqs, recorded{path: r.URL.Path, headers: r.Header.Clone(), body: b})
		status := rc.status
		rc.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) setStatus(s int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.status = s
}

func (rc *receiver) got() []recorded {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]recorded(nil), rc.reqs...)
}

// clock is a settable time for the monitor.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func create(t *testing.T, c *client.Client, kind api.Kind, name string, spec any) api.Resource {
	t.Helper()
	r, err := c.CreateResource(ctx, kind, name, spec)
	if err != nil {
		t.Fatalf("create %s/%s: %v", kind, name, err)
	}
	return testenv.WaitReady(t, c, r.ID)
}

func last(t *testing.T, d api.MetricData, metric string) float64 {
	t.Helper()
	for _, s := range d.Items {
		if s.Metric == metric {
			if len(s.Points) == 0 {
				t.Fatalf("%s has no points", metric)
			}
			return s.Points[len(s.Points)-1].V
		}
	}
	t.Fatalf("no %s series in %+v", metric, d)
	return 0
}

func TestSamplesBecomeRatesGraphsAndPrometheusGauges(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	app := create(t, c, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80})
	create(t, c, api.KindDomain, "web-local", api.DomainSpec{Hostname: "web.localhost", App: "web", TLS: api.TLSInternal})
	m := env.Monitor
	clk := &clock{t: time.Now().Add(-5 * time.Minute)}
	m.Now = clk.now

	env.Fake.SetStats(app.ID, engine.ContainerStats{ID: "c1", CPUSeconds: 10, MemoryBytes: 100 << 20, MemoryLimit: 256 << 20, NetRx: 1000})
	env.Fake.SetEdgeRequests("web.localhost", engine.RequestCounters{Requests: 100, Errors5xx: 1, DurationSeconds: 2})
	if err := m.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	clk.add(30 * time.Second)
	env.Fake.SetStats(app.ID, engine.ContainerStats{ID: "c1", CPUSeconds: 25, MemoryBytes: 120 << 20, MemoryLimit: 256 << 20, NetRx: 31000})
	env.Fake.SetEdgeRequests("web.localhost", engine.RequestCounters{Requests: 400, Errors5xx: 4, DurationSeconds: 5})
	if err := m.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}

	d, err := c.ResourceMetrics(ctx, app.ID, nil, clk.now().Add(-10*time.Minute), clk.now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for metric, want := range map[string]float64{"cpu": 0.5, "memory": 120 << 20, "netRx": 1000, "requests": 10, "errors5xx": 0.1, "latency": 10, "restarts": 0} {
		if got := last(t, d, metric); fmt.Sprintf("%.4f", got) != fmt.Sprintf("%.4f", want) {
			t.Errorf("%s = %v, want %v", metric, got, want)
		}
	}
	if d.StepSeconds != 30 {
		t.Errorf("step %d for 11 minutes", d.StepSeconds)
	}
	md, err := c.MachineMetrics(ctx, []string{"cpu", "diskFree"}, clk.now().Add(-10*time.Minute), clk.now().Add(time.Minute))
	if err != nil || last(t, md, "cpu") < 0.5 || last(t, md, "diskFree") <= 0 {
		t.Errorf("machine: %+v %v", md, err)
	}
	if _, err := c.ResourceMetrics(ctx, app.ID, []string{"queueDepth"}, time.Time{}, time.Time{}); client.Code(err) != api.CodeValidationFailed {
		t.Errorf("an app's queue depth: %v", err)
	}
	if _, err := c.ResourceMetrics(ctx, app.ID, nil, time.Now(), time.Now().Add(-time.Hour)); client.Code(err) != api.CodeValidationFailed {
		t.Errorf("from after to: %v", err)
	}
	viewer, _ := env.Token(t, api.ScopeRead)
	if _, err := viewer.ResourceMetrics(ctx, app.ID, nil, time.Time{}, time.Time{}); err != nil {
		t.Errorf("a viewer reads metrics: %v", err)
	}

	res, err := http.Get(env.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `enclave_resource_value{kind="app",metric="cpu",name="web",project="default"} 0.5`) {
		t.Errorf("/metrics lacks the app's cpu gauge")
	}

	// A day later, the sweep rolls the points up and older queries read rollups (MET-04).
	clk.add(25 * time.Hour)
	m.Sweep(ctx)
	old, err := c.ResourceMetrics(ctx, app.ID, []string{"cpu"}, time.Now().Add(-48*time.Hour), time.Now()) // the API judges age by the real clock
	if err != nil {
		t.Fatal(err)
	}
	if old.StepSeconds%900 != 0 || len(old.Items[0].Points) == 0 || old.Items[0].Points[0].Max == nil {
		t.Errorf("rollups: step %d points %+v", old.StepSeconds, old.Items[0].Points)
	}
}

// A new install has points but no rollups yet. Ranges that read rollups (the console's 24h, 7d and 30d)
// fill the time after the newest rollup from points, so they show the samples instead of "no data".
func TestLongRangesShowPointsNotYetRolledUp(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	app := create(t, c, api.KindApp, "web", api.AppSpec{Image: "nginx", Port: 80})
	clk := &clock{t: time.Now().Add(-2 * time.Minute)}
	env.Monitor.Now = clk.now
	for i, cpu := range []float64{10, 25, 40} {
		env.Fake.SetStats(app.ID, engine.ContainerStats{ID: "c1", CPUSeconds: cpu, MemoryBytes: 100 << 20, MemoryLimit: 256 << 20})
		if err := env.Monitor.SampleOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			clk.add(30 * time.Second)
		}
	}
	// The console asks for a range a moment longer than points are kept, which reads rollups.
	for _, span := range []time.Duration{24*time.Hour + time.Second, 7 * 24 * time.Hour} {
		d, err := c.ResourceMetrics(ctx, app.ID, []string{"cpu"}, time.Now().Add(-span), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if d.StepSeconds%900 != 0 || len(d.Items) != 1 || len(d.Items[0].Points) == 0 {
			t.Errorf("%s: step %d, items %+v", span, d.StepSeconds, d.Items)
		}
	}
}

func TestCrashLoopAlertFiresOnceSurvivesARestartAndResolves(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	hook := newReceiver(t)
	app := create(t, c, api.KindApp, "web", api.AppSpec{Image: "nginx"})
	create(t, c, api.KindSecret, "hook-key", api.SecretSpec{Value: "signing-key"})
	ch := create(t, c, api.KindChannel, "ops-hook", api.ChannelSpec{Type: api.ChannelWebhook, Webhook: &api.WebhookTarget{URL: hook.srv.URL + "/alerts", Secret: "hook-key"}})
	alert := create(t, c, api.KindAlert, "web-crashing", api.AlertSpec{Condition: api.AlertCondition{Type: api.ConditionCrashLoop, Resources: []string{"app/web"}}, Channels: []string{"ops-hook"}})
	m := env.Monitor
	clk := &clock{t: time.Now().Add(-30 * time.Minute)}
	m.Now = clk.now
	step := func(restarts int) {
		t.Helper()
		env.Fake.SetStats(app.ID, engine.ContainerStats{ID: "c1", Restarts: restarts})
		if err := m.SampleOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := m.EvaluateOnce(ctx); err != nil {
			t.Fatal(err)
		}
		m.NotifyOnce(ctx)
		clk.add(30 * time.Second)
	}
	step(0)
	step(1)
	if n := len(hook.got()); n != 0 {
		t.Fatalf("notified before three restarts: %d", n)
	}
	step(3)
	got := hook.got()
	if len(got) != 1 {
		t.Fatalf("want one firing notification, got %d", len(got))
	}
	var p monitor.Payload
	_ = json.Unmarshal(got[0].body, &p)
	if p.Event != api.EventFiring || p.Alert != "web-crashing" || p.Resource != "app/web" || !strings.Contains(p.Message, "restarted 3 times") {
		t.Errorf("payload %+v", p)
	}
	if sig := got[0].headers.Get(webhook.SignatureHeader); sig != webhook.Signature("signing-key", got[0].body) {
		t.Errorf("signature %q", sig)
	}
	if r, _ := c.GetResource(ctx, alert.ID); r.Outputs["state"] != api.AlertFiring {
		t.Errorf("alert outputs %v", r.Outputs)
	}

	// Still firing: no second notification, and a restarted monitor doesn't announce it again (ALR-08).
	step(3)
	restarted := &monitor.Monitor{Store: m.Store, Box: m.Box, Driver: m.Driver, Metrics: m.Metrics, Log: m.Log, Now: clk.now, HTTP: m.HTTP}
	if err := restarted.EvaluateOnce(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.NotifyOnce(ctx)
	if n := len(hook.got()); n != 1 {
		t.Fatalf("re-announced: %d notifications", n)
	}

	// Ten quiet minutes later the restarts left the window: resolved.
	clk.add(11 * time.Minute)
	step(3)
	got = hook.got()
	if len(got) != 2 {
		t.Fatalf("want a resolution, got %d notifications", len(got))
	}
	_ = json.Unmarshal(got[1].body, &p)
	if p.Event != api.EventResolved || p.ResolvedAt == nil {
		t.Errorf("resolution %+v", p)
	}
	incidents, err := c.Incidents(ctx, alert.ID, 10)
	if err != nil || len(incidents) != 1 || incidents[0].ResolvedAt == nil || len(incidents[0].Notifications) != 2 || incidents[0].Notifications[0].Status != api.NotificationSent {
		t.Errorf("incidents %+v %v", incidents, err)
	}
	if _, err := c.DeleteResource(ctx, ch.ID, nil); client.Code(err) != api.CodeResourceInUse {
		t.Errorf("deleting a channel an alert uses: %v", err)
	}
}

func TestMetricAlertOnTheMachineAndNoData(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	hook := newReceiver(t)
	create(t, c, api.KindChannel, "hook", api.ChannelSpec{Type: api.ChannelWebhook, Webhook: &api.WebhookTarget{URL: hook.srv.URL}})
	zero := -1.0 // the disk is always more than -1 % full
	a := create(t, c, api.KindAlert, "disk", api.AlertSpec{Condition: api.AlertCondition{Type: api.ConditionMetric, Resources: []string{api.MachineRef},
		Metric: api.MetricDiskPercentFull, Operator: api.OperatorAbove, Threshold: &zero, ForMinutes: 1}, Channels: []string{"hook"}})
	gone := create(t, c, api.KindAlert, "ghost", api.AlertSpec{Condition: api.AlertCondition{Type: api.ConditionFailed, Resources: []string{"app/nope"}}, Channels: []string{"hook"}})
	m := env.Monitor
	clk := &clock{t: time.Now().Add(-10 * time.Minute)}
	m.Now = clk.now
	for range 4 {
		if err := m.SampleOnce(ctx); err != nil {
			t.Fatal(err)
		}
		clk.add(30 * time.Second)
	}
	if err := m.EvaluateOnce(ctx); err != nil {
		t.Fatal(err)
	}
	m.NotifyOnce(ctx)
	if r, _ := c.GetResource(ctx, a.ID); r.Outputs["state"] != api.AlertFiring {
		t.Errorf("machine disk alert: %v", r.Outputs)
	}
	if r, _ := c.GetResource(ctx, gone.ID); r.Outputs["state"] != api.AlertNoData {
		t.Errorf("an alert on a missing resource: %v", r.Outputs)
	}
	if len(hook.got()) != 1 {
		t.Fatalf("notifications: %d", len(hook.got()))
	}
	// Notifications read like people write: the metric's title and unit, "this machine", "1 minute",
	// and no link to a console only this machine can open (testenv listens on 127.0.0.1; NCH-02).
	var p monitor.Payload
	_ = json.Unmarshal(hook.got()[0].body, &p)
	if !strings.HasPrefix(p.Message, "Disk full of this machine has been above -1.0% for 1 minute (now ") || !strings.HasSuffix(p.Message, "%).") {
		t.Errorf("message: %q", p.Message)
	}
	if p.Subject() != "[EnclaveStack] FIRING: disk on this machine" || p.Resource != api.MachineRef || p.URL != "" || strings.Contains(p.Text(), "127.0.0.1") {
		t.Errorf("subject %q, resource %q, url %q", p.Subject(), p.Resource, p.URL)
	}
	// Turned off, it says so and stops.
	off := false
	if _, err := c.UpdateResource(ctx, a.ID, map[string]any{"enabled": &off}, nil); err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, c, a.ID)
	_ = m.EvaluateOnce(ctx)
	if r, _ := c.GetResource(ctx, a.ID); r.Outputs["state"] != api.AlertDisabled {
		t.Errorf("disabled alert: %v", r.Outputs)
	}
}

func TestNotificationsRetryThenFailWithAnEvent(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	hook := newReceiver(t)
	hook.setStatus(500)
	ch := create(t, c, api.KindChannel, "flaky", api.ChannelSpec{Type: api.ChannelWebhook, Webhook: &api.WebhookTarget{URL: hook.srv.URL}})
	m := env.Monitor
	clk := &clock{t: time.Now()}
	m.Now = clk.now
	err := env.Store.WithTx(ctx, func(q *store.Queries) error {
		_, err := monitor.Queue(ctx, q, ch.ID, "", monitor.Payload{Event: api.EventFiring, Alert: "a", Resource: "app/x"}, clk.now())
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, wait := range append([]time.Duration{0}, monitor.RetryDelays...) {
		clk.add(wait)
		m.NotifyOnce(ctx)
	}
	list, err := c.Notifications(ctx, ch.ID, 10)
	if err != nil || len(list) != 1 || list[0].Status != api.NotificationFailed || list[0].Attempts != 6 || !strings.Contains(list[0].LastError, "500") {
		t.Fatalf("after the retries: %+v %v", list, err)
	}
	if len(hook.got()) != 6 {
		t.Errorf("attempts reaching the receiver: %d", len(hook.got()))
	}
	evs, _ := c.Events(ctx, ch.ID, 20)
	found := false
	for _, e := range evs {
		found = found || e.Type == "notification_failed"
	}
	if !found {
		t.Error("no event when giving up")
	}
}

func TestEveryChannelTypeDeliversATest(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	hooks := newReceiver(t)
	m := env.Monitor
	m.TelegramAPI = hooks.srv.URL
	stop := make(chan struct{})
	defer close(stop)
	go func() { // the notifier, as the server runs it
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				m.NotifyOnce(ctx)
			}
		}
	}()
	create(t, c, api.KindSecret, "slack-url", api.SecretSpec{Value: hooks.srv.URL + "/slack"})
	create(t, c, api.KindSecret, "discord-url", api.SecretSpec{Value: hooks.srv.URL + "/discord"})
	create(t, c, api.KindSecret, "bot-token", api.SecretSpec{Value: "123:abc"})
	create(t, c, api.KindEmail, "mailer", api.EmailSpec{})
	specs := map[string]api.ChannelSpec{
		"slack":    {Type: api.ChannelSlack, Secret: "slack-url"},
		"discord":  {Type: api.ChannelDiscord, Secret: "discord-url"},
		"telegram": {Type: api.ChannelTelegram, Secret: "bot-token", ChatID: "-100"},
		"mail":     {Type: api.ChannelEmail, Email: &api.ChannelEmailSettings{Service: "mailer", From: "alerts@shop.test", To: []string{"oncall@shop.test"}}},
	}
	for name, spec := range specs {
		ch := create(t, c, api.KindChannel, name+"-ch", spec)
		res, err := c.Test(ctx, ch.ID)
		if err != nil || !res.OK || len(res.Items) != 1 {
			t.Errorf("%s test: %+v %v", name, res, err)
		}
	}
	paths := map[string]string{}
	for _, r := range hooks.got() {
		paths[r.path] = string(r.body)
	}
	if !strings.Contains(paths["/slack"], `"text":"[EnclaveStack] Test of slack-ch`) || !strings.Contains(paths["/discord"], `"content":`) {
		t.Errorf("slack/discord bodies: %v", paths)
	}
	if b := paths["/bot123:abc/sendMessage"]; !strings.Contains(b, `"chat_id":"-100"`) {
		t.Errorf("telegram: %v", paths)
	}
	svc, _ := c.FindResource(ctx, api.KindEmail, "mailer")
	list, err := c.Emails(ctx, svc.ID, 10)
	if err != nil || list.Total != 1 || !strings.Contains(list.Items[0].Subject, "Test of mail-ch") {
		t.Errorf("email channel: %+v %v", list, err)
	}

	// A test that fails says why straight away, without retrying (NCH-05).
	hooks.setStatus(404)
	ch, _ := c.FindResource(ctx, api.KindChannel, "slack-ch")
	if res, err := c.Test(ctx, ch.ID); err != nil || res.OK || !strings.Contains(res.Items[0].Message, "404") {
		t.Errorf("failing test: %+v %v", res, err)
	}
}

func TestLogExportSendsEachLineOnceAndCatchesUp(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	dest := newReceiver(t)
	loki := newReceiver(t)
	app := create(t, c, api.KindApp, "api", api.AppSpec{Image: "nginx"})
	x := create(t, c, api.KindLogExport, "to-http", api.LogExportSpec{Destination: api.LogDestination{HTTP: &api.LogHTTP{URL: dest.srv.URL}}, Filter: "^keep"})
	lk := create(t, c, api.KindLogExport, "to-loki", api.LogExportSpec{Sources: []string{"app/api"}, Destination: api.LogDestination{Loki: &api.LogHTTP{URL: loki.srv.URL, Labels: map[string]string{"env": "test"}}}})
	bucket := create(t, c, api.KindBucket, "logs", api.BucketSpec{})
	bx := create(t, c, api.KindLogExport, "archive", api.LogExportSpec{Destination: api.LogDestination{Bucket: &api.BucketFolder{Bucket: "logs", Prefix: "shop"}}})
	m := env.Monitor
	line := func(msg string) api.LogLine {
		return api.LogLine{Time: time.Now().UTC(), Stream: "stdout", Message: msg}
	}
	env.Fake.AddLogs(app.ID, "es-app-api-r1-0", line("keep 1"), line("drop me"), line("keep 2"))
	ship := func(r api.Resource) error {
		t.Helper()
		fresh, _ := env.Store.GetResource(ctx, r.ID)
		return m.ShipOnce(ctx, fresh)
	}
	for _, r := range []api.Resource{x, lk, bx} {
		if err := ship(r); err != nil {
			t.Fatalf("ship %s: %v", r.Name, err)
		}
	}
	got := dest.got()
	if len(got) != 1 || strings.Count(string(got[0].body), "\n") != 2 || strings.Contains(string(got[0].body), "drop me") {
		t.Fatalf("http batch: %d %s", len(got), got[0].body)
	}
	var first monitor.ExportLine
	_ = json.Unmarshal(bytes.SplitN(got[0].body, []byte("\n"), 2)[0], &first)
	if first.Project != "default" || first.Kind != "app" || first.Name != "api" || first.Container != "es-app-api-r1-0" || first.Message != "keep 1" {
		t.Errorf("line %+v", first)
	}
	var push struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][2]string       `json:"values"`
		} `json:"streams"`
	}
	lg := loki.got()
	if len(lg) != 1 || lg[0].path != "/loki/api/v1/push" || json.Unmarshal(lg[0].body, &push) != nil || len(push.Streams) != 1 ||
		push.Streams[0].Stream["env"] != "test" || push.Streams[0].Stream["name"] != "api" || len(push.Streams[0].Values) != 3 {
		t.Errorf("loki push: %+v", lg)
	}
	bk, _ := c.GetResource(ctx, bucket.ID)
	target := backup.Target{Endpoint: fmt.Sprint(bk.Outputs["endpoint"]), Bucket: "logs"}
	keys := env.Backups.Objects(target)
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "shop/") || !strings.HasSuffix(keys[0], ".ndjson.gz") {
		t.Fatalf("bucket objects %v", keys)
	}
	st, _ := env.Backups.Open(ctx, target)
	rc, _ := st.Get(ctx, keys[0])
	gz, err := gzip.NewReader(rc)
	if err != nil {
		t.Fatal(err)
	}
	unzipped, _ := io.ReadAll(gz)
	if strings.Count(string(unzipped), "\n") != 3 {
		t.Errorf("archived lines: %s", unzipped)
	}

	// Nothing new: nothing sent. The destination fails: the lines wait, then arrive once.
	if err := ship(x); err != nil || len(dest.got()) != 1 {
		t.Errorf("an empty pass sent something: %d %v", len(dest.got()), err)
	}
	env.Fake.AddLogs(app.ID, "es-app-api-r1-0", line("keep 3"))
	dest.setStatus(503)
	if err := ship(x); err == nil {
		t.Fatal("a failing destination reported success")
	}
	if r, _ := c.GetResource(ctx, x.ID); !strings.Contains(fmt.Sprint(r.Outputs["lastError"]), "503") {
		t.Errorf("lastError: %v", r.Outputs)
	}
	dest.setStatus(200)
	if err := ship(x); err != nil {
		t.Fatal(err)
	}
	got = dest.got()
	if b := string(got[len(got)-1].body); !strings.Contains(b, "keep 3") || strings.Contains(b, "keep 1") {
		t.Errorf("catch-up batch: %s", b)
	}
	if r, _ := c.GetResource(ctx, x.ID); r.Outputs["lastError"] != "" || fmt.Sprint(r.Outputs["linesSent"]) != "3" {
		t.Errorf("after recovery: %v", r.Outputs)
	}
	if _, err := c.DeleteResource(ctx, bucket.ID, nil); client.Code(err) != api.CodeResourceInUse {
		t.Errorf("deleting a bucket an export writes to: %v", err)
	}
}

// TestLogExportKeepsLinesThatShareATimeAndReportsRotationGaps: lines with the same timestamp are each
// sent once even when a pass ends among them, and lines the engine dropped before they were read are
// reported as a gap (LOG-06, ADR-040).
func TestLogExportKeepsLinesThatShareATimeAndReportsRotationGaps(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	dest := newReceiver(t)
	app := create(t, c, api.KindApp, "api", api.AppSpec{Image: "nginx"})
	x := create(t, c, api.KindLogExport, "out", api.LogExportSpec{Destination: api.LogDestination{HTTP: &api.LogHTTP{URL: dest.srv.URL}}})
	const box = "es-app-api-r1-0"
	ship := func() {
		t.Helper()
		fresh, _ := env.Store.GetResource(ctx, x.ID)
		if err := env.Monitor.ShipOnce(ctx, fresh); err != nil {
			t.Fatal(err)
		}
	}
	sent := func() []string {
		var out []string
		for _, g := range dest.got() {
			for _, l := range strings.Split(strings.TrimSpace(string(g.body)), "\n") {
				var e monitor.ExportLine
				_ = json.Unmarshal([]byte(l), &e)
				out = append(out, e.Message)
			}
		}
		return out
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	same := func(msg string) api.LogLine { return api.LogLine{Time: at, Stream: "stdout", Message: msg} }
	env.Fake.AddLogs(app.ID, box, same("a1"), same("a2"))
	ship()
	env.Fake.AddLogs(app.ID, box, same("a3"), api.LogLine{Time: at.Add(time.Second), Stream: "stdout", Message: "b"})
	ship()
	ship()
	if got := strings.Join(sent(), ","); got != "a1,a2,a3,b" {
		t.Fatalf("lines sharing a time: %s", got)
	}

	// Rotation drops lines that were never read, including the checkpoint's: a gap event, then the rest.
	env.Fake.AddLogs(app.ID, box, api.LogLine{Time: at.Add(2 * time.Second), Stream: "stdout", Message: "lost"},
		api.LogLine{Time: at.Add(3 * time.Second), Stream: "stdout", Message: "c"})
	env.Fake.RotateLogs(app.ID, box, at.Add(3*time.Second))
	ship()
	if got := strings.Join(sent(), ","); got != "a1,a2,a3,b,c" {
		t.Fatalf("after rotation: %s", got)
	}
	evs, err := env.Store.ListEvents(ctx, x.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	var gap string
	for _, e := range evs {
		if e.Type == "export_gap" {
			gap = e.Message
		}
	}
	if !strings.Contains(gap, box) || !strings.Contains(gap, "no longer kept") {
		t.Errorf("gap event: %q", gap)
	}
	ship() // a quiet pass is not a gap
	n := 0
	evs, _ = env.Store.ListEvents(ctx, x.ID, 50)
	for _, e := range evs {
		if e.Type == "export_gap" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("gap events: %d", n)
	}
}

// Each machine has its own series of what runs on it (L3-002 §11.2, v0.8).
func TestEachMachineHasItsOwnMetrics(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	rack := env.AddAgent(t, c, "rack-1", nil)
	env.WaitMachine(t, c, "rack-1", api.MachineOnline)
	there := create(t, c, api.KindApp, "far", api.AppSpec{Image: "nginx", Placed: api.Placed{Machine: "rack-1"}})
	here := create(t, c, api.KindApp, "near", api.AppSpec{Image: "nginx", Placed: api.Placed{Machine: testenv.MainName()}})
	m := env.Monitor
	clk := &clock{t: time.Now().Add(-5 * time.Minute)}
	m.Now = clk.now
	rack.Driver.SetStats(there.ID, engine.ContainerStats{ID: "c1", CPUSeconds: 10, MemoryBytes: 300 << 20})
	env.Fake.SetStats(here.ID, engine.ContainerStats{ID: "c2", CPUSeconds: 10, MemoryBytes: 100 << 20})
	if err := m.SampleOnce(ctx); err != nil {
		t.Fatal(err)
	}
	from, to := clk.now().Add(-time.Minute), clk.now().Add(time.Minute)
	far, err := c.MachineMetricsOf(ctx, "rack-1", []string{"memory", "diskFree"}, from, to)
	if err != nil || last(t, far, "memory") != 300<<20 || len(far.Items[1].Points) != 0 {
		t.Errorf("rack-1: %+v %v", far, err)
	}
	main, err := c.MachineMetricsOf(ctx, testenv.MainName(), []string{"memory"}, from, to)
	if err != nil || last(t, main, "memory") != 100<<20 {
		t.Errorf("main: %+v %v", main, err)
	}
	if old, err := c.MachineMetrics(ctx, []string{"memory"}, from, to); err != nil || last(t, old, "memory") != 100<<20 {
		t.Errorf("/machine/metrics is still the main machine's: %+v %v", old, err)
	}
	if _, err := c.MachineMetricsOf(ctx, "nowhere", nil, from, to); client.Code(err) != api.CodeNotFound {
		t.Errorf("an unknown machine: %v", err)
	}
}
