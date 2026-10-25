package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/mail"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/webhook"
	"github.com/enclavestack/platform/pkg/api"
)

// The notifier (ADR-039): a persisted outbox, sent every 2 s, retried after 15, 30, 60, 120 and 240 s.

// Notifier timing.
const (
	notifyTick      = 2 * time.Second
	notifyBatch     = 20
	KeptPerChannel  = 100
	TelegramDefault = "https://api.telegram.org"
)

// RetryDelays are the waits before each retry; after the last, a notification fails (NCH-04).
var RetryDelays = []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute}

// Payload is what a notification says (NCH-02, NCH-03); the webhook body is its JSON.
type Payload struct {
	Event      string     `json:"event"`
	Alert      string     `json:"alert"`
	Project    string     `json:"project"`
	Resource   string     `json:"resource"`
	State      string     `json:"state"`
	Value      *float64   `json:"value"`
	Threshold  *float64   `json:"threshold"`
	Message    string     `json:"message"`
	StartedAt  time.Time  `json:"startedAt"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	URL        string     `json:"url,omitempty"`
}

// Subject is a notification's one-line summary.
func (p Payload) Subject() string {
	switch p.Event {
	case api.EventTest:
		return fmt.Sprintf("[EnclaveStack] Test of %s", p.Alert)
	case api.EventResolved:
		return fmt.Sprintf("[EnclaveStack] RESOLVED: %s on %s", p.Alert, api.RefText(p.Resource))
	}
	return fmt.Sprintf("[EnclaveStack] FIRING: %s on %s", p.Alert, api.RefText(p.Resource))
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Text is a notification as plain text.
func (p Payload) Text() string {
	var b strings.Builder
	b.WriteString(p.Subject() + "\n")
	if p.Message != "" {
		b.WriteString(p.Message + "\n")
	}
	if p.Project != "" {
		fmt.Fprintf(&b, "Project: %s\n", p.Project)
	}
	if p.Event != api.EventTest {
		fmt.Fprintf(&b, "Started: %s\n", p.StartedAt.UTC().Format("2 Jan 2006 15:04 UTC"))
	}
	if p.ResolvedAt != nil {
		fmt.Fprintf(&b, "Resolved: %s\n", p.ResolvedAt.UTC().Format("2 Jan 2006 15:04 UTC"))
	}
	if p.URL != "" {
		b.WriteString(p.URL + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Queue adds a notification for a channel to the outbox; the caller's transaction makes it durable.
func Queue(ctx context.Context, q *store.Queries, channelID, incidentID string, p Payload, now time.Time) (string, error) {
	body, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	n := store.Notification{ID: domain.NewID("ntf"), ChannelID: channelID, IncidentID: incidentID, Event: p.Event, Alert: p.Alert, Subject: p.Resource,
		BodyJSON: string(body), Status: api.NotificationPending, NextAttemptAt: now, CreatedAt: now}
	return n.ID, q.InsertNotification(ctx, n)
}

func (m *Monitor) notifyLoop(ctx context.Context) {
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		m.NotifyOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// NotifyOnce sends every due notification once.
func (m *Monitor) NotifyOnce(ctx context.Context) {
	due, err := m.Store.DueNotifications(ctx, m.Now(), notifyBatch)
	if err != nil {
		if ctx.Err() == nil {
			m.Log.Warn("read the notification outbox failed", "error", err)
		}
		return
	}
	for _, n := range due {
		m.deliver(ctx, n)
	}
}

func (m *Monitor) deliver(ctx context.Context, n store.Notification) {
	ch, err := m.Store.GetResource(ctx, n.ChannelID)
	if err != nil {
		return // the channel went: its rows went with it
	}
	var p Payload
	_ = json.Unmarshal([]byte(n.BodyJSON), &p)
	spec, _, specErr := api.NormalizeSpec(api.KindChannel, ch.Spec)
	if specErr == nil {
		err = m.Send(ctx, ch, spec.(api.ChannelSpec), p)
	} else {
		err = specErr
	}
	now := m.Now()
	n.Attempts++
	typ := "unknown"
	if specErr == nil {
		typ = spec.(api.ChannelSpec).Type
	}
	switch {
	case err == nil:
		n.Status, n.SentAt, n.LastError = api.NotificationSent, &now, ""
		m.Metrics.Notifications.WithLabelValues(typ, "sent").Inc()
	case n.Attempts > len(RetryDelays) || n.Event == api.EventTest || errors.Is(err, errPermanent):
		n.Status, n.LastError = api.NotificationFailed, truncate(err.Error())
		m.Metrics.Notifications.WithLabelValues(typ, "failed").Inc()
		if n.Event != api.EventTest {
			m.event(ctx, ch, "notification_failed", fmt.Sprintf("Gave up on the %s notification of %s for %s after %d attempts: %s", n.Event, n.Alert, n.Subject, n.Attempts, n.LastError))
		}
	default:
		n.LastError, n.NextAttemptAt = truncate(err.Error()), now.Add(RetryDelays[n.Attempts-1])
		m.Metrics.Notifications.WithLabelValues(typ, "retry").Inc()
	}
	if err := m.Store.UpdateNotification(ctx, n); err != nil {
		m.Log.Warn("record a notification attempt failed", "notification_id", n.ID, "error", err)
	}
	_ = m.Store.PruneNotifications(ctx, ch.ID, KeptPerChannel)
}

var errPermanent = errors.New("permanent")

// Send delivers one notification through a channel now.
func (m *Monitor) Send(ctx context.Context, ch *domain.Resource, s api.ChannelSpec, p Payload) error {
	switch s.Type {
	case api.ChannelEmail:
		return m.sendEmail(ctx, ch, s, p)
	case api.ChannelWebhook:
		body, _ := json.Marshal(p)
		headers := map[string]string{"Content-Type": "application/json", "X-Enclave-Event": p.Event}
		if s.Webhook.Secret != "" {
			key, err := m.secretValue(ctx, ch.ProjectName(), s.Webhook.Secret)
			if err != nil {
				return err
			}
			headers[webhook.SignatureHeader] = webhook.Signature(key, body)
		}
		return webhook.Post(ctx, m.HTTP, s.Webhook.URL, body, headers)
	case api.ChannelSlack, api.ChannelDiscord:
		target, err := m.secretValue(ctx, ch.ProjectName(), s.Secret)
		if err != nil {
			return err
		}
		target = strings.TrimSpace(target)
		if !api.ValidHTTPURL(target) {
			return fmt.Errorf("%w: the secret %s doesn't hold a webhook URL", errPermanent, s.Secret)
		}
		key := "text"
		if s.Type == api.ChannelDiscord {
			key = "content"
		}
		body, _ := json.Marshal(map[string]string{key: p.Text()})
		return webhook.Post(ctx, m.HTTP, target, body, map[string]string{"Content-Type": "application/json"})
	case api.ChannelTelegram:
		token, err := m.secretValue(ctx, ch.ProjectName(), s.Secret)
		if err != nil {
			return err
		}
		base := m.TelegramAPI
		if base == "" {
			base = TelegramDefault
		}
		body, _ := json.Marshal(map[string]string{"chat_id": s.ChatID, "text": p.Text()})
		err = webhook.Post(ctx, m.HTTP, strings.TrimRight(base, "/")+"/bot"+url.PathEscape(strings.TrimSpace(token))+"/sendMessage", body,
			map[string]string{"Content-Type": "application/json"})
		if err != nil {
			// The URL holds the token: never let it reach a message (L3-006 §9).
			return errors.New(strings.ReplaceAll(err.Error(), strings.TrimSpace(token), "<token>"))
		}
		return nil
	}
	return fmt.Errorf("%w: unknown channel type %q", errPermanent, s.Type)
}

func (m *Monitor) sendEmail(ctx context.Context, ch *domain.Resource, s api.ChannelSpec, p Payload) error {
	svc, err := m.Store.GetResourceByName(ctx, ch.ProjectName(), api.KindEmail, s.Email.Service)
	if err != nil {
		return fmt.Errorf("the email service %s doesn't exist", s.Email.Service)
	}
	if svc.Phase != api.PhaseReady {
		return fmt.Errorf("the email service %s isn't ready", s.Email.Service)
	}
	pw, err := m.secretValue(ctx, svc.ProjectName(), domain.PasswordSecretFor(api.KindEmail, svc.Name))
	if err != nil {
		return err
	}
	var es api.EmailSpec
	_ = json.Unmarshal(svc.Spec, &es)
	ep, err := mail.EndpointFrom(svc.ID, svc.Name, pw, svc.Outputs, engine.OutputWebPort, es.Mode == api.EmailRelaying)
	if err != nil {
		return err
	}
	if m.Mail == nil {
		return errors.New("this control plane can't send email")
	}
	_, err = m.Mail.Send(ctx, ep, api.SendEmail{From: s.Email.From, To: s.Email.To, Subject: p.Subject(), Text: p.Text()})
	return err
}

// secretValue opens a secret of a project.
func (m *Monitor) secretValue(ctx context.Context, project, name string) (string, error) {
	sec, err := m.Store.GetResourceByName(ctx, project, api.KindSecret, name)
	if err != nil {
		return "", fmt.Errorf("the secret %s doesn't exist", name)
	}
	v, err := m.Box.Open(sec.SecretCiphertext, []byte(sec.ID))
	if err != nil {
		return "", fmt.Errorf("the secret %s can't be decrypted", name)
	}
	return string(v), nil
}

func (m *Monitor) event(ctx context.Context, r *domain.Resource, typ, msg string) {
	if err := m.Store.AddEvent(ctx, r.ID, typ, truncate(msg), m.Now()); err != nil {
		m.Log.Warn("record event failed", "resource_id", r.ID, "error", err)
	}
}

func truncate(s string) string {
	const max = 512
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
