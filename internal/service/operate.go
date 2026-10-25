package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/monitor"
	"github.com/enclavestack/platform/internal/secretstores"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The v0.7 kinds' references (L3-002 §10.8) and their data endpoints and actions (§10.5).

// testWait bounds how long the test action waits for the notifier (ADR-038).
const testWait = 15 * time.Second

// operateRefs checks what an alert, channel, log export, secret store or synced secret names, and
// references it (CP-12). Dashboards and alerts don't reference the resources they show or watch.
func (s *Resources) operateRefs(ctx context.Context, q *store.Queries, r *domain.Resource, spec any) ([]store.Ref, error) {
	project := r.ProjectName()
	var refs []store.Ref
	need := func(kind api.Kind, name, field string) error {
		res, err := live(ctx, q, project, kind, name, field)
		if err != nil {
			return err
		}
		refs = append(refs, store.Ref{ToID: res.ID, Field: field})
		return nil
	}
	switch sp := spec.(type) {
	case api.AlertSpec:
		for i, ch := range sp.Channels {
			if err := need(api.KindChannel, ch, fmt.Sprintf("spec.channels[%d]", i)); err != nil {
				return nil, err
			}
		}
	case api.ChannelSpec:
		switch {
		case sp.Email != nil:
			if err := need(api.KindEmail, sp.Email.Service, "spec.email.service"); err != nil {
				return nil, err
			}
		case sp.Webhook != nil && sp.Webhook.Secret != "":
			if err := need(api.KindSecret, sp.Webhook.Secret, "spec.webhook.secret"); err != nil {
				return nil, err
			}
		case sp.Secret != "":
			if err := need(api.KindSecret, sp.Secret, "spec.secret"); err != nil {
				return nil, err
			}
		}
	case api.LogExportSpec:
		d := sp.Destination
		var err error
		switch {
		case d.Bucket != nil:
			err = need(api.KindBucket, d.Bucket.Bucket, "spec.destination.bucket.bucket")
		case d.S3 != nil:
			err = need(api.KindSecret, d.S3.CredentialsSecret, "spec.destination.s3.credentialsSecret")
		default:
			for field, h := range map[string]*api.LogHTTP{"http": d.HTTP, "otlp": d.OTLP, "loki": d.Loki} {
				if h != nil && h.HeadersSecret != "" {
					err = need(api.KindSecret, h.HeadersSecret, "spec.destination."+field+".headersSecret")
				}
			}
		}
		if err != nil {
			return nil, err
		}
	case api.SecretStoreSpec:
		field, name := sp.CredentialSecret()
		if err := need(api.KindSecret, name, field); err != nil {
			return nil, err
		}
	case api.SecretSpec:
		if sp.Source != nil {
			if err := need(api.KindSecretStore, sp.Source.Store, "spec.source.store"); err != nil {
				return nil, err
			}
		}
	}
	return dedupeRefs(refs), nil
}

// TestAction runs the test action of an alert, channel or secret store (ALR-06, NCH-05, EXT-07).
func (s *Resources) TestAction(ctx context.Context, p Principal, id string) (api.TestResult, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.TestResult{}, notFound()
	}
	if err != nil {
		return api.TestResult{}, err
	}
	switch r.Kind {
	case api.KindSecretStore:
		return s.testStore(ctx, r), nil
	case api.KindAlert, api.KindChannel:
	default:
		return api.TestResult{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s can't be tested.", r.Kind))
	}
	channels := []*domain.Resource{r}
	alert := r.Name
	if r.Kind == api.KindAlert {
		raw, _, err := api.NormalizeSpec(api.KindAlert, r.Spec)
		if err != nil {
			return api.TestResult{}, err
		}
		channels = nil
		for _, name := range raw.(api.AlertSpec).Channels {
			ch, err := s.Store.GetResourceByName(ctx, r.ProjectName(), api.KindChannel, name)
			if err == nil {
				channels = append(channels, ch)
			}
		}
	}
	now := s.Now()
	payload := monitor.Payload{Event: api.EventTest, Alert: alert, Project: r.ProjectName(), Resource: r.Ref(), State: "test", StartedAt: now,
		Message: "This is a test notification from EnclaveStack, sent by " + p.Actor() + "."}
	ids := map[string]string{}
	err = s.Store.WithTx(ctx, func(q *store.Queries) error {
		for _, ch := range channels {
			nid, err := monitor.Queue(ctx, q, ch.ID, "", payload, now)
			if err != nil {
				return err
			}
			ids[nid] = ch.Name
		}
		return nil
	})
	if err != nil {
		return api.TestResult{}, err
	}
	out := api.TestResult{OK: true, Items: []api.TestOutcome{}}
	deadline := time.Now().Add(testWait)
	for nid, name := range ids {
		item := api.TestOutcome{Target: "channel/" + name, Message: "The notifier didn't answer within 15 seconds; see the channel's deliveries."}
		for time.Now().Before(deadline) {
			n, err := s.Store.GetNotification(ctx, nid)
			if err != nil {
				break
			}
			if n.Status != api.NotificationPending {
				item.OK, item.Message = n.Status == api.NotificationSent, n.LastError
				if item.OK {
					item.Message = "Delivered."
				}
				break
			}
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
		out.OK = out.OK && item.OK
		out.Items = append(out.Items, item)
	}
	if len(out.Items) == 0 {
		out.OK = false
		out.Items = append(out.Items, api.TestOutcome{Target: r.Ref(), Message: "This alert has no channels that exist."})
	}
	_ = s.Store.AddEvent(ctx, r.ID, "action_test", fmt.Sprintf("Test sent by %s.", p.Actor()), now)
	return out, nil
}

func (s *Resources) testStore(ctx context.Context, r *domain.Resource) api.TestResult {
	c, _, err := secretstores.ForSpec(r.Spec, func(name string) (string, error) { return s.secretOf(ctx, r.ProjectName(), name) }, &http.Client{}, s.Now)
	if err == nil {
		err = c.Test(ctx)
	}
	item := api.TestOutcome{Target: r.Ref(), OK: err == nil, Message: "Connected; the credentials work."}
	if err != nil {
		item.Message = err.Error()
	}
	return api.TestResult{OK: item.OK, Items: []api.TestOutcome{item}}
}

// secretOf opens a secret of a project.
func (s *Resources) secretOf(ctx context.Context, project, name string) (string, error) {
	sec, err := s.Store.GetResourceByName(ctx, project, api.KindSecret, name)
	if err != nil {
		return "", fmt.Errorf("the secret %s doesn't exist", name)
	}
	v, err := s.Box.Open(sec.SecretCiphertext, []byte(sec.ID))
	if err != nil {
		return "", fmt.Errorf("the secret %s can't be decrypted", name)
	}
	return string(v), nil
}

// refresh reads a synced secret from its store now (EXT-03).
func (s *Resources) refresh(ctx context.Context, p Principal, id string) (api.Resource, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return api.Resource{}, notFound()
	}
	if err != nil {
		return api.Resource{}, err
	}
	var spec api.SecretSpec
	if raw, _, err := api.NormalizeSpec(r.Kind, r.Spec); r.Kind == api.KindSecret && err == nil {
		spec = raw.(api.SecretSpec)
	}
	if spec.Source == nil {
		return api.Resource{}, api.NewError(api.CodeActionNotSupported, "Only a secret from a secret store can be refreshed.")
	}
	if r.Phase != api.PhaseReady {
		return api.Resource{}, api.NewError(api.CodeActionNotSupported, "Wait until it's ready, or use Retry if it failed.")
	}
	now := s.Now()
	if err := s.Store.SetNextRun(ctx, id, &now); err != nil {
		return api.Resource{}, err
	}
	_ = s.Store.AddEvent(ctx, id, "action_refresh", "Refresh requested by "+p.Actor()+".", now)
	r.NextRunAt = &now
	return r.ToAPI(), nil
}

// Incidents lists an alert's incidents with each channel's outcome (ALR-05).
func (s *Resources) Incidents(ctx context.Context, id string, limit int) (api.IncidentList, error) {
	r, err := s.Store.GetResource(ctx, id)
	if err != nil {
		return api.IncidentList{}, notFound()
	}
	if r.Kind != api.KindAlert {
		return api.IncidentList{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no incidents.", r.Kind))
	}
	rows, err := s.Store.ListIncidents(ctx, id, limit)
	if err != nil {
		return api.IncidentList{}, err
	}
	ids := make([]string, len(rows))
	for i, in := range rows {
		ids[i] = in.ID
	}
	notes, err := s.Store.IncidentNotifications(ctx, ids)
	if err != nil {
		return api.IncidentList{}, err
	}
	names := map[string]string{}
	byIncident := map[string][]api.IncidentNotification{}
	for _, n := range notes {
		if _, ok := names[n.ChannelID]; !ok {
			names[n.ChannelID] = n.ChannelID
			if ch, err := s.Store.GetResource(ctx, n.ChannelID); err == nil {
				names[n.ChannelID] = ch.Name
			}
		}
		byIncident[n.IncidentID] = append(byIncident[n.IncidentID], api.IncidentNotification{Channel: names[n.ChannelID], Event: n.Event, Status: n.Status, Error: n.LastError})
	}
	out := api.IncidentList{Items: make([]api.Incident, 0, len(rows))}
	for _, in := range rows {
		item := api.Incident{ID: in.ID, Subject: in.Subject, StartedAt: in.StartedAt, ResolvedAt: in.ResolvedAt, Value: in.Value, Message: in.Message,
			Notifications: byIncident[in.ID]}
		if item.Notifications == nil {
			item.Notifications = []api.IncidentNotification{}
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// Notifications lists a channel's newest deliveries (NCH-04).
func (s *Resources) Notifications(ctx context.Context, id string, limit int) (api.NotificationList, error) {
	r, err := s.Store.GetResource(ctx, id)
	if err != nil {
		return api.NotificationList{}, notFound()
	}
	if r.Kind != api.KindChannel {
		return api.NotificationList{}, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no deliveries.", r.Kind))
	}
	rows, err := s.Store.ChannelNotifications(ctx, id, limit)
	if err != nil {
		return api.NotificationList{}, err
	}
	out := api.NotificationList{Items: make([]api.Notification, 0, len(rows))}
	for _, n := range rows {
		out.Items = append(out.Items, api.Notification{ID: n.ID, Event: n.Event, Alert: n.Alert, Subject: n.Subject, Status: n.Status, Attempts: n.Attempts,
			LastError: n.LastError, CreatedAt: n.CreatedAt, SentAt: n.SentAt})
	}
	return out, nil
}
