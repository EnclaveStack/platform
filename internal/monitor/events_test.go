package monitor_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/monitor"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// Event alerts (ADR-060, L2-016 EVN-01 – EVN-03): each matching event is sent once, from the time the
// alert was made.
func TestEventAlertSendsEachMatchingEventOnce(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, "")
	ctx := context.Background()
	hook := newReceiver(t)
	app := create(t, c, api.KindApp, "web", api.AppSpec{Image: "nginx"}) // its "ready" comes before the alert
	create(t, c, api.KindChannel, "ops-hook", api.ChannelSpec{Type: api.ChannelWebhook, Webhook: &api.WebhookTarget{URL: hook.srv.URL + "/events"}})
	alert := create(t, c, api.KindAlert, "web-events", api.AlertSpec{Condition: api.AlertCondition{Type: api.ConditionEvent,
		Resources: []string{"app/web"}, Events: []string{"redeployed", "drift_detected"}}, Channels: []string{"ops-hook"}})
	m := env.Monitor
	cycle := func() {
		t.Helper()
		if err := m.EvaluateOnce(ctx); err != nil {
			t.Fatal(err)
		}
		m.NotifyOnce(ctx)
	}
	cycle()
	if n := len(hook.got()); n != 0 {
		t.Fatalf("events from before the alert were sent: %d", n)
	}
	if err := env.Monitor.Store.AddEvent(ctx, app.ID, "redeployed", "Redeployed revision 2.", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.Monitor.Store.AddEvent(ctx, app.ID, "ready", "Ready.", time.Now()); err != nil { // not chosen
		t.Fatal(err)
	}
	cycle()
	cycle()
	got := hook.got()
	if len(got) != 1 {
		t.Fatalf("want one notification, got %d", len(got))
	}
	var p monitor.Payload
	_ = json.Unmarshal(got[0].body, &p)
	if p.Event != api.EventEvent || p.Type != "redeployed" || p.Resource != "app/web" || p.Message != "Redeployed revision 2." {
		t.Errorf("payload %+v", p)
	}
	if !strings.HasPrefix(p.Subject(), "[EnclaveStack] REDEPLOYED: web-events on") {
		t.Errorf("subject %q", p.Subject())
	}
	if r, _ := c.GetResource(ctx, alert.ID); r.Outputs["state"] != api.AlertOK {
		t.Errorf("alert outputs %v", r.Outputs)
	}

	// Turned off, events are missed; turned on again, it starts from the newest.
	off := false
	if _, err := c.UpdateResource(ctx, alert.ID, map[string]any{"enabled": off}, nil); err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, c, alert.ID)
	cycle()
	_ = env.Monitor.Store.AddEvent(ctx, app.ID, "drift_detected", "Missed.", time.Now())
	cycle()
	if _, err := c.UpdateResource(ctx, alert.ID, map[string]any{"enabled": true}, nil); err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, c, alert.ID)
	cycle()
	_ = env.Monitor.Store.AddEvent(ctx, app.ID, "drift_detected", "Seen.", time.Now())
	cycle()
	got = hook.got()
	if len(got) != 2 {
		t.Fatalf("want two notifications, got %d", len(got))
	}
	_ = json.Unmarshal(got[1].body, &p)
	if p.Message != "Seen." {
		t.Errorf("second %+v", p)
	}
}
