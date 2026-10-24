package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"regexp"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/transcode"
	"github.com/enclavestack/platform/internal/webhook"
	"github.com/enclavestack/platform/pkg/api"
)

// Transcoder notifications and tags (L2-012 TRX-05 – TRX-08, ADR-050).

// DefaultNotifyBackoff is when a transcoder's webhook is tried: at once, after 2 and after 8 more seconds.
var DefaultNotifyBackoff = []time.Duration{0, 2 * time.Second, 8 * time.Second}

var tagName = regexp.MustCompile(api.TagNamePatternSource)

// inputTags are the tags the uploader set on an input (TRX-06): lowercase names of letters, digits and
// hyphens, without the enclave-* keys EnclaveStack writes on its own outputs.
func inputTags(meta map[string]string) map[string]string {
	tags := map[string]string{}
	for k, v := range meta {
		if k = strings.ToLower(k); tagName.MatchString(k) && !strings.HasPrefix(k, "enclave-") {
			tags[k] = v
		}
	}
	return tags
}

// outputMeta is what every output carries: the input's tags and where it came from.
func outputMeta(tags map[string]string, res *domain.Resource, run *domain.Run) map[string]string {
	meta := maps.Clone(tags)
	maps.Copy(meta, map[string]string{"enclave-transcoder": res.Name, "enclave-run": run.ID, "enclave-source": run.Key})
	return meta
}

// transcodeResult is what a run learned, for its message.
type transcodeResult struct {
	inputSize int64
	tags      map[string]string
	outputs   []api.TranscodeObject
	master    *string
}

// notifyTranscode sends a finished run's message to the transcoder's webhook and topic (TRX-05). A message
// that can't be delivered is an event on the transcoder; the run's status stays (TRX-08).
func (r *Reconciler) notifyTranscode(ctx context.Context, res *domain.Resource, s api.TranscoderSpec, run *domain.Run, got transcodeResult, log *slog.Logger) {
	n := s.Notify
	if n == nil || run.FinishedAt == nil {
		return
	}
	tags := got.tags
	if tags == nil {
		tags = map[string]string{}
	}
	outputs := got.outputs
	if outputs == nil {
		outputs = []api.TranscodeObject{}
	}
	body, err := json.Marshal(api.TranscodeMessage{Event: api.TranscodeEvent, Transcoder: res.Name, Project: res.Project, RunID: run.ID,
		Status: run.Status, Message: run.Message, Input: api.TranscodeObject{Bucket: s.Input.Bucket, Key: run.Key, Size: got.inputSize},
		Outputs: outputs, HLSMaster: got.master, Metadata: tags, StartedAt: run.StartedAt.UTC(), FinishedAt: run.FinishedAt.UTC()})
	if err != nil {
		return
	}
	if w := n.Webhook; w != nil {
		if err := r.postTranscodeWebhook(ctx, res, w, run, body); err != nil {
			r.event(ctx, res, "notify_failed", fmt.Sprintf("The webhook %s failed after %d tries for run %s: %s", w.URL, len(r.notifyBackoff()), run.ID, truncate(err.Error())))
			log.Warn("transcode webhook failed", "error", err)
		}
	}
	if n.Topic != "" {
		if err := r.publishTranscode(ctx, res.Project, n.Topic, run, body); err != nil {
			r.event(ctx, res, "notify_failed", fmt.Sprintf("Publishing run %s to the topic %s failed: %s", run.ID, n.Topic, truncate(err.Error())))
			log.Warn("transcode topic publish failed", "error", err)
		}
	}
}

func (r *Reconciler) notifyBackoff() []time.Duration {
	if len(r.NotifyBackoff) > 0 {
		return r.NotifyBackoff
	}
	return DefaultNotifyBackoff
}

func (r *Reconciler) postTranscodeWebhook(ctx context.Context, res *domain.Resource, w *api.TranscodeWebhook, run *domain.Run, body []byte) error {
	headers := map[string]string{"Content-Type": "application/json", "X-Enclave-Event": api.TranscodeEvent, "X-Enclave-Transcoder": res.Name, "X-Enclave-Run-Id": run.ID}
	if w.Secret != "" {
		key, err := r.secretValue(ctx, res.Project, w.Secret)
		if err != nil {
			return err
		}
		headers[webhook.SignatureHeader] = webhook.Signature(key, body)
	}
	var err error
	for _, wait := range r.notifyBackoff() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		if err = webhook.Post(ctx, r.HTTP, w.URL, body, headers); err == nil {
			return nil
		}
	}
	return err
}

func (r *Reconciler) publishTranscode(ctx context.Context, project, topic string, run *domain.Run, body []byte) error {
	if _, err := r.Store.GetResourceByName(ctx, project, api.KindTopic, topic); err != nil {
		return fmt.Errorf("the topic no longer exists")
	}
	b, err := r.broker(ctx)
	if err != nil {
		return err
	}
	_, err = b.Publish(ctx, domain.BrokerKey(project, topic), messaging.Message{Body: string(body), Attributes: map[string]string{"event": api.TranscodeEvent, "status": run.Status}})
	return err
}

// masterKey is the key of the ladder's master playlist when the run wrote it.
func masterKey(s api.TranscoderSpec, folder string, written []api.TranscodeObject) *string {
	if s.HLS == nil {
		return nil
	}
	want := folder + transcode.MasterPlaylist(s.HLS)
	for _, o := range written {
		if o.Key == want {
			return &want
		}
	}
	return nil
}
