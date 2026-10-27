package reconciler_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// Functions (ADR-055, L2-016 FN-01 – FN-12) through the real API on the fake engine.

func eventOf(t *testing.T, r api.Run) api.FunctionEvent {
	t.Helper()
	var ev api.FunctionEvent
	if err := json.Unmarshal(r.Event, &ev); err != nil {
		t.Fatalf("run %s event %s: %v", r.ID, r.Event, err)
	}
	return ev
}

// runsBy waits until a function has a finished run with the trigger, and returns it.
func runBy(t *testing.T, c *client.Client, id, trigger string) api.Run {
	t.Helper()
	var got api.Run
	waitFor(t, 15*time.Second, func() bool {
		runs, _ := c.ListRuns(context.Background(), id, 50, "")
		for _, r := range runs.Items {
			if r.Trigger == trigger && r.Status != api.RunRunning {
				got = r
				return true
			}
		}
		return false
	})
	return got
}

func TestFunctionInvokeQueueTopicBucket(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	create(t, c, api.KindBucket, "uploads", api.BucketSpec{})
	uploads := backup.Target{Endpoint: "http://127.0.0.1:41999", Bucket: "uploads"}
	env.Backups.Put(uploads, "in/old.png", []byte("old"))
	create(t, c, api.KindQueue, "jobs", api.QueueSpec{VisibilityTimeoutSeconds: 120})
	create(t, c, api.KindQueue, "quick", api.QueueSpec{VisibilityTimeoutSeconds: 30})

	// A queue that would show a message again while its run goes on is refused (FN-09).
	_, err := c.CreateResource(ctx, api.KindFunction, "slowq", api.FunctionSpec{Image: "alpine:3.22", Triggers: []api.FunctionTrigger{{Queue: "quick"}}})
	if apiCode(err) != api.CodeValidationFailed {
		t.Errorf("a queue with a short visibility timeout: %v", err)
	}

	fn := create(t, c, api.KindFunction, "resize", api.FunctionSpec{Image: "alpine:3.22", Command: []string{"sh", "-c", "echo ok"},
		Env: map[string]string{"MODE": "fast"}, Triggers: []api.FunctionTrigger{{Queue: "jobs"}, {Bucket: "uploads", Prefix: "in/"}}})
	if fn.Outputs["triggers"] != float64(2) || fn.Status.Steps[len(fn.Status.Steps)-1].Name != "ready" {
		t.Errorf("outputs %v steps %v", fn.Outputs, fn.Status.Steps)
	}

	// Invoked: the body is the event, written to the container's event file with the ENCLAVE_ variables (FN-05, FN-06).
	d, err := c.Invoke(ctx, fn.ID, map[string]any{"size": 3}, true)
	if err != nil || d.Status != api.RunSucceeded || d.Trigger != api.TriggerInvoke {
		t.Fatalf("invoke: %+v %v", d, err)
	}
	if ev := eventOf(t, d.Run); ev.Source != api.SourceInvoke || string(ev.Body) != `{"size":3}` {
		t.Errorf("event %s", d.Event)
	}
	jr, ok := env.Fake.JobRunOf(d.ID)
	if !ok || string(jr.Files[api.EventPath]) != string(d.Event) || jr.Env[api.EnvEventFile] != api.EventPath || jr.Env[api.EnvEventSource] != "invoke" ||
		jr.Env[api.EnvRunID] != d.ID || jr.Env[api.EnvFunction] != "resize" || jr.Env["MODE"] != "fast" {
		t.Errorf("container: %+v", jr)
	}

	// A queue message starts a run and is deleted when it succeeds (FN-08).
	jobs, _ := c.FindResource(ctx, api.KindQueue, "jobs")
	if _, err := c.SendMessage(ctx, jobs.ID, api.SendMessage{Body: "photo-1"}); err != nil {
		t.Fatal(err)
	}
	qr := runBy(t, c, fn.ID, api.TriggerQueue)
	if ev := eventOf(t, qr); qr.Status != api.RunSucceeded || ev.Queue != "jobs" {
		t.Errorf("queue run %+v %s", qr, qr.Event)
	}
	waitFor(t, 5*time.Second, func() bool {
		left, _ := c.Messages(ctx, jobs.ID, 10)
		return len(left.Items) == 0
	})

	// A new object in the folder starts a run; the ones there before don't (FN-10).
	time.Sleep(300 * time.Millisecond)
	env.Backups.Put(uploads, "in/cat.png", []byte("cat"))
	br := runBy(t, c, fn.ID, api.TriggerBucket)
	if ev := eventOf(t, br); ev.Bucket != "uploads" || ev.Key != "in/cat.png" || ev.Size == nil || *ev.Size != 3 {
		t.Errorf("bucket run %s", br.Event)
	}
	runs, _ := c.ListRuns(ctx, fn.ID, 50, "")
	for _, r := range runs.Items {
		if r.Trigger == api.TriggerBucket && r.Key == "in/old.png" {
			t.Error("an object from before the baseline started a run")
		}
	}

	// A topic subscription reaches the function; the topic keeps it from being deleted (FN-03).
	topic := create(t, c, api.KindTopic, "events", api.TopicSpec{Subscriptions: []api.Subscription{{Function: "resize"}}})
	if _, err := c.SendMessage(ctx, topic.ID, api.SendMessage{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	tr := runBy(t, c, fn.ID, api.TriggerTopic)
	if ev := eventOf(t, tr); tr.Status != api.RunSucceeded || ev.Topic != "events" {
		t.Errorf("topic run %+v %s", tr, tr.Event)
	}
	if _, err := c.DeleteResource(ctx, fn.ID, nil); apiCode(err) != api.CodeResourceInUse {
		t.Errorf("deleting a function a topic sends to: %v", err)
	}
	// Its output is in its runs, not logs.
	if _, err := c.Logs(ctx, fn.ID, 10); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("logs: %v", err)
	}
}

func TestFunctionConcurrencyAndSchedule(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	fn := create(t, c, api.KindFunction, "slow", api.FunctionSpec{Image: "alpine:3.22", Command: []string{"sleep 2"}, Concurrency: 1,
		Triggers: []api.FunctionTrigger{{Schedule: "@daily", TimeZone: "Europe/Berlin"}}})
	if fn.Outputs["nextRunAt"] == nil {
		t.Errorf("next run not reported: %v", fn.Outputs)
	}
	first, err := c.Invoke(ctx, fn.ID, nil, false)
	if err != nil || first.Status != api.RunRunning {
		t.Fatalf("invoke: %+v %v", first, err)
	}
	// At its concurrency: 429 FUNCTION_BUSY with Retry-After, for invocations and manual runs (FN-07).
	_, err = c.Invoke(ctx, fn.ID, "x", false)
	if e, ok := err.(*api.Error); !ok || e.Code != api.CodeFunctionBusy || e.RetryAfterSeconds != api.FunctionBusyRetrySeconds {
		t.Errorf("a second invocation: %v", err)
	}
	if _, err := c.RunNow(ctx, fn.ID); apiCode(err) != api.CodeFunctionBusy {
		t.Errorf("a manual run: %v", err)
	}
	if d := waitRun(t, c, fn.ID, first.ID); d.Status != api.RunSucceeded || eventOf(t, d.Run).Source != api.SourceInvoke || string(eventOf(t, d.Run).Body) != "null" {
		t.Errorf("first %+v %s", d, d.Event)
	}
	run, err := c.RunNow(ctx, fn.ID)
	if err != nil || eventOf(t, run).Source != api.SourceManual {
		t.Errorf("manual run %+v %v", run, err)
	}
	// Invoking anything else isn't supported.
	db := create(t, c, api.KindQueue, "plain", api.QueueSpec{})
	if _, err := c.Invoke(ctx, db.ID, nil, false); apiCode(err) != api.CodeActionNotSupported {
		t.Errorf("invoking a queue: %v", err)
	}
}
