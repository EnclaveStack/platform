package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/mail"
	"github.com/enclavestack/platform/internal/messaging"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// The v0.6 rules that need other resources (L3-002 §9.4, §9.5), and the data endpoints of queues, topics
// and email services (§9.3). Services only send, receive and read here; the broker's streams, users
// and the containers belong to the reconciler.

// strongPassword is a managed password with the character classes OpenSearch requires (ADR-031): 32
// random URL-safe characters (192 bits) and a fixed tail. It never holds ":" or " ", which Mailpit's
// credentials format splits on.
func strongPassword() string { return randomPassword() + "-Hc7" }

// dataRefs checks what a v0.6 resource names and references it (CP-12).
func (s *Resources) dataRefs(ctx context.Context, q *store.Queries, r *domain.Resource, spec any) ([]store.Ref, error) {
	project := r.ProjectName()
	switch sp := spec.(type) {
	case api.QueueSpec:
		if sp.DeadLetterQueue == "" {
			return nil, nil
		}
		if sp.DeadLetterQueue == r.Name {
			return nil, api.Validation([]api.FieldError{{Field: "spec.deadLetterQueue", Code: api.FieldNotAllowed, Message: "A queue can't be its own dead-letter queue."}})
		}
		dlq, err := live(ctx, q, project, api.KindQueue, sp.DeadLetterQueue, "spec.deadLetterQueue")
		if err != nil {
			return nil, err
		}
		return []store.Ref{{ToID: dlq.ID, Field: "spec.deadLetterQueue"}}, nil
	case api.TopicSpec:
		var refs []store.Ref
		for i, sub := range sp.Subscriptions {
			f := fmt.Sprintf("spec.subscriptions[%d]", i)
			if sub.Queue != "" {
				qu, err := live(ctx, q, project, api.KindQueue, sub.Queue, f+".queue")
				if err != nil {
					return nil, err
				}
				refs = append(refs, store.Ref{ToID: qu.ID, Field: f + ".queue"})
			}
			if sub.Secret != "" {
				sec, err := live(ctx, q, project, api.KindSecret, sub.Secret, f+".secret")
				if err != nil {
					return nil, err
				}
				refs = append(refs, store.Ref{ToID: sec.ID, Field: f + ".secret"})
			}
		}
		return dedupeRefs(refs), nil
	case api.SearchSpec:
		return s.placementRefs(ctx, q, project, sp.Network, sp.Subnet, false)
	case api.EmailSpec:
		refs, err := s.placementRefs(ctx, q, project, sp.Network, sp.Subnet, false)
		if err != nil || sp.Relay == nil {
			return refs, err
		}
		sec, err := live(ctx, q, project, api.KindSecret, sp.Relay.CredentialsSecret, "spec.relay.credentialsSecret")
		if err != nil {
			return nil, err
		}
		return append(refs, store.Ref{ToID: sec.ID, Field: "spec.relay.credentialsSecret"}), nil
	case api.TranscoderSpec:
		var refs []store.Ref
		for _, f := range []struct{ bucket, field string }{{sp.Input.Bucket, "spec.input.bucket"}, {sp.Output.Bucket, "spec.output.bucket"}} {
			b, err := live(ctx, q, project, api.KindBucket, f.bucket, f.field)
			if err != nil {
				return nil, err
			}
			refs = append(refs, store.Ref{ToID: b.ID, Field: f.field})
		}
		return dedupeRefs(refs), nil
	case api.CDNSpec:
		return s.cdnRefs(ctx, q, r, sp)
	}
	return nil, nil
}

// cdnRefs checks a CDN's origin, its hostname against every domain and CDN, and its zone.
func (s *Resources) cdnRefs(ctx context.Context, q *store.Queries, r *domain.Resource, c api.CDNSpec) ([]store.Ref, error) {
	project := r.ProjectName()
	kind, name, _ := api.ParseOrigin(c.Origin)
	origin, err := live(ctx, q, project, kind, name, "spec.origin")
	if err != nil {
		return nil, err
	}
	if kind == api.KindApp && c.OriginPort == 0 {
		var as api.AppSpec
		if json.Unmarshal(origin.Spec, &as) != nil || as.Port == 0 {
			return nil, api.Validation([]api.FieldError{{Field: "spec.originPort", Code: api.FieldRequired,
				Message: "The app " + name + " has no port. Give the app a port, or set the origin port here."}})
		}
	}
	if err := s.hostnameFree(ctx, q, r, c.Hostname); err != nil {
		return nil, err
	}
	refs := []store.Ref{{ToID: origin.ID, Field: "spec.origin"}}
	zone, err := s.domainZoneRef(ctx, q, project, c.AsDomain())
	return append(refs, zone...), err
}

// hostnameFree refuses a hostname another domain or CDN serves (CDN-01).
func (s *Resources) hostnameFree(ctx context.Context, q *store.Queries, r *domain.Resource, hostname string) error {
	for _, kind := range []api.Kind{api.KindDomain, api.KindCDN} {
		others, err := q.ResourcesOfKind(ctx, kind)
		if err != nil {
			return err
		}
		for _, o := range others {
			if o.ID == r.ID {
				continue
			}
			var h struct {
				Hostname string `json:"hostname"`
			}
			if json.Unmarshal(o.Spec, &h) != nil || h.Hostname != hostname {
				continue
			}
			msg := fmt.Sprintf("The %s %s already serves %s.", o.Kind, o.Name, hostname)
			return &api.Error{Code: api.CodeHostnameTaken, Message: msg, Details: []api.FieldError{{Field: "spec.hostname", Code: api.FieldNotAllowed, Message: msg}}}
		}
	}
	return nil
}

// dataResource loads a resource for a data endpoint: one of kinds, and ready.
func (s *Resources) dataResource(ctx context.Context, id string, kinds ...api.Kind) (*domain.Resource, error) {
	r, err := s.Store.GetResource(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound()
	}
	if err != nil {
		return nil, err
	}
	ok := false
	for _, k := range kinds {
		ok = ok || r.Kind == k
	}
	if !ok {
		return nil, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("A %s has no messages here.", r.Kind))
	}
	if r.Phase != api.PhaseReady && r.Phase != api.PhaseUpdating {
		return nil, api.NewError(api.CodeActionNotSupported, fmt.Sprintf("Wait until the %s is ready, or use Retry if it failed.", r.Kind))
	}
	return r, nil
}

// broker returns the broker, translating its errors for clients.
func (s *Resources) broker(ctx context.Context) (messaging.Broker, error) {
	if s.Messaging == nil {
		return nil, api.NewError(api.CodeEngineUnavailable, "This server has no message broker.")
	}
	b, err := s.Messaging.Broker(ctx)
	if err != nil {
		return nil, brokerError(err)
	}
	return b, nil
}

func brokerError(err error) error {
	switch {
	case errors.Is(err, messaging.ErrUnavailable):
		e := api.NewError(api.CodeEngineUnavailable, "The message broker isn't reachable right now. EnclaveStack is bringing it back; try again shortly.")
		e.RetryAfterSeconds = 5
		return e
	case errors.Is(err, messaging.ErrNotFound):
		e := api.NewError(api.CodeEngineUnavailable, "This queue or topic is missing on the broker; EnclaveStack is restoring it. Try again shortly.")
		e.RetryAfterSeconds = 5
		return e
	}
	return err
}

func toMessage(m api.SendMessage) messaging.Message {
	return messaging.Message{Body: m.Body, Attributes: m.Attributes, DedupID: m.DeduplicationID}
}

// SendMessage sends to a queue or publishes to a topic (QUE-05, TOP-06).
func (s *Resources) SendMessage(ctx context.Context, p Principal, id string, m api.SendMessage) (api.SentMessage, error) {
	r, err := s.dataResource(ctx, id, api.KindQueue, api.KindTopic)
	if err != nil {
		return api.SentMessage{}, err
	}
	limit := api.MessageBytesMax
	if r.Kind == api.KindQueue {
		raw, _, err := api.NormalizeSpec(api.KindQueue, r.Spec)
		if err != nil {
			return api.SentMessage{}, err
		}
		limit = raw.(api.QueueSpec).MaxMessageBytes
	}
	if errs := api.ValidateSendMessage(m, limit); len(errs) > 0 {
		return api.SentMessage{}, api.Validation(errs)
	}
	b, err := s.broker(ctx)
	if err != nil {
		return api.SentMessage{}, err
	}
	var sent messaging.Sent
	if r.Kind == api.KindQueue {
		sent, err = b.Send(ctx, domain.BrokerKey(r.Project, r.Name), toMessage(m))
	} else {
		sent, err = b.Publish(ctx, domain.BrokerKey(r.Project, r.Name), toMessage(m))
	}
	if err != nil {
		return api.SentMessage{}, brokerError(err)
	}
	if s.Metrics != nil {
		s.Metrics.MessagesSent.WithLabelValues(string(r.Kind)).Inc()
	}
	s.Log.Info("message sent", "resource_id", r.ID, "kind", r.Kind, "by", p.Key(), "duplicate", sent.Duplicate) // never the body
	return api.SentMessage{ID: sent.ID, Duplicate: sent.Duplicate}, nil
}

func peeked(m messaging.Received) api.PeekedMessage {
	attrs := m.Attributes
	if attrs == nil {
		attrs = map[string]string{}
	}
	return api.PeekedMessage{ID: m.ID, Body: m.Body, Attributes: attrs, SentAt: m.SentAt.UTC()}
}

// Messages returns a queue's live stats and its oldest messages, without receiving them (QUE-05, QUE-06).
func (s *Resources) Messages(ctx context.Context, id string, limit int) (api.MessageList, error) {
	r, err := s.dataResource(ctx, id, api.KindQueue)
	if err != nil {
		return api.MessageList{}, err
	}
	b, err := s.broker(ctx)
	if err != nil {
		return api.MessageList{}, err
	}
	st, err := b.QueueStats(ctx, domain.BrokerKey(r.Project, r.Name))
	if err != nil {
		return api.MessageList{}, brokerError(err)
	}
	list, err := b.Peek(ctx, domain.BrokerKey(r.Project, r.Name), limit)
	if err != nil {
		return api.MessageList{}, brokerError(err)
	}
	out := api.MessageList{Stats: api.QueueStats{Messages: st.Messages, InFlight: st.InFlight}, Items: make([]api.PeekedMessage, 0, len(list))}
	if st.Oldest != nil {
		age := int64(s.Now().Sub(*st.Oldest).Seconds())
		out.Stats.OldestAgeSeconds = &age
	}
	if c, err := s.Store.SubscriptionCounts(ctx, r.ID); err == nil {
		out.Stats.DeadLettered = c[store.DeadLetterTarget].Delivered
	}
	for _, m := range list {
		out.Items = append(out.Items, peeked(m))
	}
	return out, nil
}

// ReceiveMessages takes up to max messages from a queue, waiting up to waitSeconds (QUE-02, QUE-05).
func (s *Resources) ReceiveMessages(ctx context.Context, id string, req api.ReceiveMessages) (api.ReceivedList, error) {
	req, errs := api.NormalizeReceive(req)
	if len(errs) > 0 {
		return api.ReceivedList{}, api.Validation(errs)
	}
	r, err := s.dataResource(ctx, id, api.KindQueue)
	if err != nil {
		return api.ReceivedList{}, err
	}
	b, err := s.broker(ctx)
	if err != nil {
		return api.ReceivedList{}, err
	}
	got, err := b.Receive(ctx, domain.BrokerKey(r.Project, r.Name), req.Max, time.Duration(req.WaitSeconds)*time.Second)
	if err != nil {
		return api.ReceivedList{}, brokerError(err)
	}
	out := api.ReceivedList{Items: make([]api.ReceivedMessage, 0, len(got))}
	for _, m := range got {
		out.Items = append(out.Items, api.ReceivedMessage{PeekedMessage: peeked(m), Receipt: m.Receipt, ReceiveCount: m.ReceiveCount})
	}
	return out, nil
}

// DeleteMessages deletes received messages by receipt; receipts that delete nothing are reported.
func (s *Resources) DeleteMessages(ctx context.Context, id string, req api.DeleteMessages) (api.DeletedMessages, error) {
	if errs := api.ValidateDeleteMessages(req); len(errs) > 0 {
		return api.DeletedMessages{}, api.Validation(errs)
	}
	r, err := s.dataResource(ctx, id, api.KindQueue)
	if err != nil {
		return api.DeletedMessages{}, err
	}
	b, err := s.broker(ctx)
	if err != nil {
		return api.DeletedMessages{}, err
	}
	out := api.DeletedMessages{Failed: []api.ReceiptFailure{}}
	for _, rc := range req.Receipts {
		switch err := b.Delete(ctx, domain.BrokerKey(r.Project, r.Name), rc); {
		case err == nil:
			out.Deleted++
		case errors.Is(err, messaging.ErrBadReceipt):
			out.Failed = append(out.Failed, api.ReceiptFailure{Receipt: rc, Message: "This receipt isn't one of this queue's, or its message is already deleted."})
		default:
			return out, brokerError(err)
		}
	}
	return out, nil
}

// Deliveries counts a topic's deliveries per subscription (TOP-07).
func (s *Resources) Deliveries(ctx context.Context, id string) (api.DeliveryStats, error) {
	r, err := s.dataResource(ctx, id, api.KindTopic)
	if err != nil {
		return api.DeliveryStats{}, err
	}
	raw, _, err := api.NormalizeSpec(api.KindTopic, r.Spec)
	if err != nil {
		return api.DeliveryStats{}, err
	}
	counts, err := s.Store.SubscriptionCounts(ctx, r.ID)
	if err != nil {
		return api.DeliveryStats{}, err
	}
	b, err := s.broker(ctx)
	if err != nil {
		return api.DeliveryStats{}, err
	}
	ts, err := b.TopicStats(ctx, domain.BrokerKey(r.Project, r.Name))
	if err != nil {
		return api.DeliveryStats{}, brokerError(err)
	}
	out := api.DeliveryStats{Published: int64(ts.Published), Items: []api.SubscriptionStats{}}
	for _, sub := range raw.(api.TopicSpec).Subscriptions {
		c := counts[sub.Target()]
		out.Items = append(out.Items, api.SubscriptionStats{Target: sub.Target(), Delivered: c.Delivered, Failed: c.Failed, LastError: c.LastError,
			Pending: ts.Pending[messaging.SubscriptionName(sub.Target())]})
	}
	return out, nil
}

// mailEndpoint is how the control plane reaches an email service: its ports on the machine and its
// managed password.
func (s *Resources) mailEndpoint(ctx context.Context, r *domain.Resource) (mail.Endpoint, api.EmailSpec, error) {
	raw, _, err := api.NormalizeSpec(api.KindEmail, r.Spec)
	if err != nil {
		return mail.Endpoint{}, api.EmailSpec{}, err
	}
	spec := raw.(api.EmailSpec)
	sec, err := s.Store.GetResourceByName(ctx, r.Project, api.KindSecret, domain.PasswordSecretFor(api.KindEmail, r.Name))
	if err != nil {
		return mail.Endpoint{}, spec, err
	}
	pw, err := s.Box.Open(sec.SecretCiphertext, []byte(sec.ID))
	if err != nil {
		return mail.Endpoint{}, spec, fmt.Errorf("open the email service's password: %w", err)
	}
	ep, err := mail.EndpointFrom(r.ID, r.Name, string(pw), r.Outputs, engine.OutputWebPort, spec.Mode == api.EmailRelaying)
	if errors.Is(err, mail.ErrNoAddress) {
		return mail.Endpoint{}, spec, api.NewError(api.CodeActionNotSupported, "This email service has no address on this machine (a network without internet publishes no ports).")
	}
	return ep, spec, err
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func mailError(err error) error {
	switch {
	case errors.Is(err, mail.ErrUnavailable):
		e := api.NewError(api.CodeEngineUnavailable, "The email service isn't reachable right now. Try again shortly.")
		e.RetryAfterSeconds = 5
		return e
	case errors.Is(err, mail.ErrNotFound):
		return api.NewError(api.CodeNotFound, "That message doesn't exist, or the service no longer keeps it.")
	}
	return err
}

// SendEmail sends a message through an email service (EML-04, EML-05).
func (s *Resources) SendEmail(ctx context.Context, p Principal, id string, m api.SendEmail) (api.SentEmail, error) {
	r, err := s.dataResource(ctx, id, api.KindEmail)
	if err != nil {
		return api.SentEmail{}, err
	}
	ep, spec, err := s.mailEndpoint(ctx, r)
	if err != nil {
		return api.SentEmail{}, err
	}
	m.From = strings.TrimSpace(m.From)
	if errs := api.ValidateSendEmail(m, spec.Domain); len(errs) > 0 {
		return api.SentEmail{}, api.Validation(errs)
	}
	if s.Mail == nil {
		return api.SentEmail{}, api.NewError(api.CodeEngineUnavailable, "This server can't reach email services.")
	}
	msgID, err := s.Mail.Send(ctx, ep, m)
	if err != nil {
		return api.SentEmail{}, mailError(err)
	}
	s.Log.Info("email sent", "resource_id", r.ID, "by", p.Key(), "recipients", len(m.To)+len(m.Cc)+len(m.Bcc)) // never the addresses or body
	return api.SentEmail{ID: msgID}, nil
}

// SendSystemEmail sends a message of EnclaveStack's own (an invite link) through an email service of a
// project, with no caller to check: the machine admin chose the service in the machine's settings.
func (s *Resources) SendSystemEmail(ctx context.Context, project, service string, m api.SendEmail) error {
	r, err := s.Store.GetResourceByName(ctx, project, api.KindEmail, service)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("the email service %s of %s doesn't exist any more", service, project)
	}
	if err != nil {
		return err
	}
	if r.Phase != api.PhaseReady && r.Phase != api.PhaseUpdating {
		return fmt.Errorf("the email service %s isn't ready (%s)", service, r.Phase)
	}
	ep, spec, err := s.mailEndpoint(ctx, r)
	if err != nil {
		return err
	}
	if errs := api.ValidateSendEmail(m, spec.Domain); len(errs) > 0 {
		return errors.New(errs[0].Message)
	}
	if s.Mail == nil {
		return errors.New("this server can't reach email services")
	}
	if _, err := s.Mail.Send(ctx, ep, m); err != nil {
		return err
	}
	s.Log.Info("system email sent", "resource_id", r.ID, "recipients", len(m.To)) // never the address or body
	return nil
}

// Emails lists an email service's kept messages, newest first (EML-03).
func (s *Resources) Emails(ctx context.Context, id string, limit int) (api.EmailList, error) {
	r, err := s.dataResource(ctx, id, api.KindEmail)
	if err != nil {
		return api.EmailList{}, err
	}
	ep, _, err := s.mailEndpoint(ctx, r)
	if err != nil {
		return api.EmailList{}, err
	}
	if s.Mail == nil {
		return api.EmailList{}, api.NewError(api.CodeEngineUnavailable, "This server can't reach email services.")
	}
	out, err := s.Mail.List(ctx, ep, limit)
	return out, mailError(err)
}

// Email returns one kept message (EML-03).
func (s *Resources) Email(ctx context.Context, id, emailID string) (api.Email, error) {
	r, err := s.dataResource(ctx, id, api.KindEmail)
	if err != nil {
		return api.Email{}, err
	}
	ep, _, err := s.mailEndpoint(ctx, r)
	if err != nil {
		return api.Email{}, err
	}
	if s.Mail == nil {
		return api.Email{}, api.NewError(api.CodeEngineUnavailable, "This server can't reach email services.")
	}
	out, err := s.Mail.Get(ctx, ep, emailID)
	if out.Headers == nil {
		out.Headers = map[string][]string{}
	}
	return out, mailError(err)
}

// brokerFacts reports the broker on GET /machine (L3-002 §9.2).
func (s *Resources) brokerFacts(ctx context.Context, m *api.Machine) {
	st := s.Engine.BrokerStatus(ctx)
	m.Broker = api.Broker{Available: st.Available, Message: st.Message}
}

// RunTranscode converts one object now (TRN-04), within the transcoder's concurrency; the scheduler
// starts the run within a tick.
func (s *Resources) RunTranscode(ctx context.Context, p Principal, id string, req api.TranscodeRequest) (api.Run, error) {
	var run *domain.Run
	err := s.Store.WithTx(ctx, func(q *store.Queries) error {
		r, err := q.GetResource(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if r.Kind != api.KindTranscoder {
			return api.NewError(api.CodeMalformedRequest, "Only a transcoder's run takes a key.")
		}
		if r.Phase != api.PhaseReady {
			return api.NewError(api.CodeActionNotSupported, "Wait until the transcoder is ready, or use Retry if it failed.")
		}
		raw, _, err := api.NormalizeSpec(api.KindTranscoder, r.Spec)
		if err != nil {
			return err
		}
		spec := raw.(api.TranscoderSpec)
		if errs := api.ValidateTranscodeKey(req.Key, spec.Input.Prefix); len(errs) > 0 {
			return api.Validation(errs)
		}
		running, err := q.CountRunning(ctx, id)
		if err != nil {
			return err
		}
		if running >= spec.Concurrency {
			return api.NewError(api.CodeRunInProgress, fmt.Sprintf("%d of %d conversions are running. Try again when one finishes.", running, spec.Concurrency))
		}
		now := s.Now()
		run = &domain.Run{ID: domain.NewID("run"), ResourceID: id, Trigger: api.TriggerManual, Status: api.RunRunning, StartedAt: now, Key: req.Key}
		if err := q.InsertRun(ctx, run); err != nil {
			return err
		}
		return q.AddEvent(ctx, id, "action_run", fmt.Sprintf("Transcoding %s requested by %s.", req.Key, p.Actor()), now)
	})
	if err != nil {
		return api.Run{}, err
	}
	return run.ToAPI(), nil
}
