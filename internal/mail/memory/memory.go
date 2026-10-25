// Package memory keeps email services' messages in memory, for the fake engine (ADR-032).
package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/mail"
	"github.com/enclavestack/platform/pkg/api"
)

// Client is the in-memory stand-in for Mailpit. Messages are kept per endpoint key, newest last.
type Client struct {
	mu    sync.Mutex
	boxes map[string][]api.Email
	next  int
	down  bool
}

var _ mail.Client = (*Client)(nil)

// New returns an empty client.
func New() *Client { return &Client{boxes: map[string][]api.Email{}} }

// SetDown makes every call fail as an unreachable service does.
func (c *Client) SetDown(down bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.down = down
}

// Deliver adds a message as if an app had sent it over SMTP.
func (c *Client) Deliver(ep mail.Endpoint, m api.SendEmail) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.add(ep, m)
}

func (c *Client) add(ep mail.Endpoint, m api.SendEmail) string {
	c.next++
	id := fmt.Sprintf("msg%06d", c.next)
	raw, _ := mail.Compose(m, time.Now())
	relay := api.RelayCaptured
	if ep.Relaying {
		relay = api.RelayRelayed
	}
	e := api.Email{EmailSummary: api.EmailSummary{ID: id, From: m.From, To: append([]string{}, m.To...), Subject: m.Subject, SentAt: time.Now().UTC(),
		SizeBytes: int64(len(raw)), Relay: relay}, Cc: append([]string{}, m.Cc...), Bcc: append([]string{}, m.Bcc...), ReplyTo: m.ReplyTo,
		Text: m.Text, HTML: m.HTML, Headers: map[string][]string{"Subject": {m.Subject}, "From": {m.From}}}
	c.boxes[ep.Key] = append(c.boxes[ep.Key], e)
	return id
}

// Send implements mail.Client.
func (c *Client) Send(_ context.Context, ep mail.Endpoint, m api.SendEmail) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return "", mail.ErrUnavailable
	}
	return c.add(ep, m), nil
}

// List implements mail.Client.
func (c *Client) List(_ context.Context, ep mail.Endpoint, limit int) (api.EmailList, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return api.EmailList{}, mail.ErrUnavailable
	}
	box := c.boxes[ep.Key]
	out := api.EmailList{Total: len(box), Items: []api.EmailSummary{}}
	for i := len(box) - 1; i >= 0 && len(out.Items) < limit; i-- {
		out.Items = append(out.Items, box[i].EmailSummary)
	}
	return out, nil
}

// Get implements mail.Client.
func (c *Client) Get(_ context.Context, ep mail.Endpoint, id string) (api.Email, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.down {
		return api.Email{}, mail.ErrUnavailable
	}
	for _, e := range c.boxes[ep.Key] {
		if e.ID == id {
			return e, nil
		}
	}
	return api.Email{}, mail.ErrNotFound
}

// Forget drops a service's messages (its container was deleted).
func (c *Client) Forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.boxes, key)
}
