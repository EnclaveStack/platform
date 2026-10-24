package reconciler_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/backup"
	"github.com/enclavestack/platform/internal/engine/fake"
	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
)

// L2-012 through the real server on the fake engine: a tagged upload becomes a ladder and a rendition in
// the folder its tag names, the outputs carry the tag, and the run's message reaches a signed webhook and
// a topic (TRX-02 – TRX-09).
func TestTranscoderLadderTagsAndNotify(t *testing.T) {
	env := testenv.Start(t)
	c, _ := env.Token(t, api.ScopeWrite)
	ctx := context.Background()
	type hit struct {
		body             []byte
		signature, event string
	}
	var mu sync.Mutex
	var hits []hit
	failing := false
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		hits = append(hits, hit{b, r.Header.Get("X-Enclave-Signature"), r.Header.Get("X-Enclave-Event")})
		if failing {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer hook.Close()
	create(t, c, api.KindBucket, "media", api.BucketSpec{})
	create(t, c, api.KindSecret, "hook-key", api.SecretSpec{Value: "s3cret"})
	inbox := create(t, c, api.KindQueue, "inbox", api.QueueSpec{})
	create(t, c, api.KindTopic, "video-events", api.TopicSpec{Subscriptions: []api.Subscription{{Queue: "inbox"}}})

	// The secret and topic must exist.
	spec := api.TranscoderSpec{Input: api.BucketFolder{Bucket: "media", Prefix: "incoming/"}, Output: api.BucketFolder{Bucket: "media", Prefix: "processed/{meta:video-id}/"},
		Renditions: []api.Rendition{{Name: "selfie", Container: api.ContainerMP4, MaxWidth: 720, MaxHeight: 1280, VideoKbps: 2500}},
		HLS:        &api.HLSLadder{Rungs: []api.HLSRung{{Name: "360p", MaxWidth: 360, MaxHeight: 640, VideoKbps: 600}, {Name: "720p", MaxWidth: 720, MaxHeight: 1280, VideoKbps: 2000}}},
		Notify:     &api.TranscodeNotify{Webhook: &api.TranscodeWebhook{URL: hook.URL + "/done", Secret: "missing"}, Topic: "video-events"}}
	_, err := c.CreateResource(ctx, api.KindTranscoder, "videos", spec)
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeValidationFailed || len(ae.Details) != 1 || ae.Details[0].Field != "spec.notify.webhook.secret" {
		t.Fatalf("an unknown secret: %v %+v", err, ae)
	}
	spec.Notify.Webhook.Secret = "hook-key"
	tc := create(t, c, api.KindTranscoder, "videos", spec)
	time.Sleep(300 * time.Millisecond) // the baseline listing

	media := backup.Target{Endpoint: "http://127.0.0.1:41999", Bucket: "media"}
	env.Backups.PutWithMeta(media, "incoming/clip.mov", []byte("portrait video"), map[string]string{"Video-Id": "v42", "enclave-run": "ignored"})
	run := waitTranscodeRun(t, c, tc.ID, "incoming/clip.mov")
	if run.Status != api.RunSucceeded || len(run.Outputs) != 6 {
		t.Fatalf("run: %+v", run)
	}
	for _, k := range []string{"processed/v42/selfie.mp4", "processed/v42/hls/master.m3u8", "processed/v42/hls/360p/index.m3u8", "processed/v42/hls/360p/segment-000.ts", "processed/v42/hls/720p/index.m3u8"} {
		if !slices.Contains(env.Backups.Objects(media), k) {
			t.Errorf("missing %s in %v", k, env.Backups.Objects(media))
		}
	}
	if m := env.Backups.Meta(media, "processed/v42/selfie.mp4"); m["video-id"] != "v42" || m["enclave-transcoder"] != "videos" || m["enclave-source"] != "incoming/clip.mov" || m["enclave-run"] != run.ID {
		t.Errorf("output tags: %v", m)
	}

	// The webhook: once, signed, with the outputs, the master playlist and the tag (TRX-05).
	waitFor(t, 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(hits) == 1 })
	mu.Lock()
	h := hits[0]
	mu.Unlock()
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(h.body)
	if h.signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) || h.event != api.TranscodeEvent {
		t.Errorf("headers: %+v", h)
	}
	var msg api.TranscodeMessage
	if err := json.Unmarshal(h.body, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Status != api.RunSucceeded || msg.RunID != run.ID || msg.Transcoder != "videos" || msg.Input.Key != "incoming/clip.mov" || msg.Input.Size != 14 ||
		msg.HLSMaster == nil || *msg.HLSMaster != "processed/v42/hls/master.m3u8" || msg.Metadata["video-id"] != "v42" || len(msg.Metadata) != 1 || len(msg.Outputs) != 6 || msg.Outputs[0].Bucket != "media" {
		t.Errorf("message: %s", h.body)
	}
	// The topic got the same body (TRX-05).
	waitFor(t, 10*time.Second, func() bool {
		l, err := c.Messages(ctx, inbox.ID, 10)
		return err == nil && len(l.Items) == 1 && l.Items[0].Body == string(h.body) && l.Items[0].Attributes["status"] == api.RunSucceeded
	})

	// A missing tag fails the run naming it; the message says failed (TRX-07).
	env.Backups.Put(media, "incoming/untagged.mov", []byte("video"))
	if run := waitTranscodeRun(t, c, tc.ID, "incoming/untagged.mov"); run.Status != api.RunFailed || !strings.Contains(run.Message, "no tag video-id") {
		t.Errorf("untagged: %+v", run)
	}
	// A file that isn't media: failed with FFmpeg's reason, and a webhook that keeps failing is tried 3
	// times and recorded, without changing the run (TRX-08).
	waitFor(t, 5*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(hits) == 2 })
	mu.Lock()
	failing = true
	mu.Unlock()
	env.Backups.PutWithMeta(media, "incoming/notes.mov", []byte(fake.NotMedia), map[string]string{"video-id": "v43"})
	bad := waitTranscodeRun(t, c, tc.ID, "incoming/notes.mov")
	if bad.Status != api.RunFailed {
		t.Errorf("not media: %+v", bad)
	}
	waitFor(t, 5*time.Second, func() bool {
		ev, err := c.Events(ctx, tc.ID, 50)
		return err == nil && slices.ContainsFunc(ev, func(e api.Event) bool {
			return e.Type == "notify_failed" && strings.Contains(e.Message, "after 3 tries for run "+bad.ID)
		})
	})
	mu.Lock()
	if len(hits) != 5 {
		t.Errorf("webhook calls: %d, want 2 + 3 tries", len(hits))
	}
	mu.Unlock()
	if got, _ := c.GetRun(ctx, tc.ID, bad.ID); got.Status != api.RunFailed || got.Message != bad.Message {
		t.Errorf("the run changed: %+v", got.Run)
	}

	// The secret and the topic it names can't be deleted (TRX-09); clearing notify frees them.
	for _, k := range []api.Kind{api.KindSecret, api.KindTopic} {
		name := map[api.Kind]string{api.KindSecret: "hook-key", api.KindTopic: "video-events"}[k]
		r, _ := c.FindResource(ctx, k, name)
		if _, err := c.DeleteResource(ctx, r.ID, nil); apiCode(err) != api.CodeResourceInUse {
			t.Errorf("deleting the %s: %v", k, err)
		}
	}
	if _, err := c.UpdateResource(ctx, tc.ID, map[string]any{"notify": nil}, nil); err != nil {
		t.Fatal(err)
	}
	sec, _ := c.FindResource(ctx, api.KindSecret, "hook-key")
	if _, err := c.DeleteResource(ctx, sec.ID, nil); err != nil {
		t.Errorf("deleting the secret after notify was cleared: %v", err)
	}
}

func waitTranscodeRun(t *testing.T, c interface {
	ListRuns(context.Context, string, int, string) (api.RunList, error)
}, id, key string) api.Run {
	t.Helper()
	var run api.Run
	waitFor(t, 10*time.Second, func() bool {
		runs, _ := c.ListRuns(context.Background(), id, 50, "")
		i := slices.IndexFunc(runs.Items, func(r api.Run) bool { return r.Key == key && r.Status != api.RunRunning })
		if i >= 0 {
			run = runs.Items[i]
		}
		return i >= 0
	})
	return run
}
