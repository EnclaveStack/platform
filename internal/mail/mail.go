// Package mail reaches an email service's Mailpit container (ADR-032): it sends over SMTP with the
// service's credentials and reads kept messages through Mailpit's API. memory stands in for the fake
// engine. Services use it for the email data endpoints.
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// ErrUnavailable means the email service can't be reached.
var ErrUnavailable = errors.New("the email service isn't reachable")

// ErrNotFound means the message doesn't exist (any more).
var ErrNotFound = errors.New("the message doesn't exist")

// Endpoint is one email service: its SMTP and API addresses on the machine and its credentials. Key
// identifies it (the resource ID); Relaying says whether it relays.
type Endpoint struct {
	Key      string
	SMTPAddr string // host:port
	APIURL   string // http://host:port
	User     string
	Password string
	Relaying bool
}

// ErrNoAddress means an email service publishes no ports on this machine (a network without internet).
var ErrNoAddress = errors.New("this email service has no address on this machine (a network without internet publishes no ports)")

// EndpointFrom builds an email service's endpoint from its outputs ("host", "port" and webPort) and its
// managed password (service data endpoints and alert emails share it).
func EndpointFrom(key, user, password string, outputs map[string]any, webPortKey string, relaying bool) (Endpoint, error) {
	host, _ := outputs["host"].(string)
	smtpPort, webPort := outputInt(outputs["port"]), outputInt(outputs[webPortKey])
	if host == "" || smtpPort == 0 {
		return Endpoint{}, ErrNoAddress
	}
	return Endpoint{Key: key, SMTPAddr: fmt.Sprintf("%s:%d", host, smtpPort), APIURL: fmt.Sprintf("http://%s:%d", host, webPort),
		User: user, Password: password, Relaying: relaying}, nil
}

func outputInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// Client sends and reads an email service's messages.
type Client interface {
	Send(ctx context.Context, ep Endpoint, m api.SendEmail) (string, error)
	List(ctx context.Context, ep Endpoint, limit int) (api.EmailList, error)
	Get(ctx context.Context, ep Endpoint, id string) (api.Email, error)
}

// Mailpit is the real client.
type Mailpit struct{ HTTP *http.Client }

var _ Client = Mailpit{}

const sendTimeout = 30 * time.Second

// Compose renders a message: plain text, HTML, or both as multipart/alternative. Bcc never appears in
// the headers. It returns the Message-ID it set.
func Compose(m api.SendEmail, now time.Time) ([]byte, string) {
	var b bytes.Buffer
	id := messageID(m.From)
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", m.From)
	h("To", strings.Join(m.To, ", "))
	if len(m.Cc) > 0 {
		h("Cc", strings.Join(m.Cc, ", "))
	}
	if m.ReplyTo != "" {
		h("Reply-To", m.ReplyTo)
	}
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", id)
	h("MIME-Version", "1.0")
	part := func(w io.Writer, ctype, body string) {
		fmt.Fprintf(w, "Content-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n%s", ctype, normalize(body))
	}
	switch {
	case m.HTML == "":
		part(&b, "text/plain", m.Text)
	case m.Text == "":
		part(&b, "text/html", m.HTML)
	default:
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		h("Content-Type", "multipart/alternative; boundary="+mw.Boundary())
		b.WriteString("\r\n")
		for _, p := range []struct{ ctype, body string }{{"text/plain", m.Text}, {"text/html", m.HTML}} {
			w, _ := mw.CreatePart(textproto.MIMEHeader{"Content-Type": {p.ctype + "; charset=utf-8"}, "Content-Transfer-Encoding": {"8bit"}})
			_, _ = io.WriteString(w, normalize(p.body))
		}
		_ = mw.Close()
		b.Write(body.Bytes())
	}
	return b.Bytes(), id
}

func normalize(body string) string {
	return strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n")
}

func messageID(from string) string {
	r := make([]byte, 12)
	_, _ = rand.Read(r)
	_, domain, _ := strings.Cut(from, "@")
	if domain == "" {
		domain = "enclave.local"
	}
	return "<" + hex.EncodeToString(r) + "@" + domain + ">"
}

// Send implements Client: SMTP with PLAIN auth (over loopback, which net/smtp allows without TLS), then
// the message's Mailpit ID, found by its Message-ID.
func (c Mailpit) Send(ctx context.Context, ep Endpoint, m api.SendEmail) (string, error) {
	msg, id := Compose(m, time.Now())
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", ep.SMTPAddr)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	host, _, _ := net.SplitHostPort(ep.SMTPAddr)
	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer cl.Close()
	if err := cl.Auth(smtp.PlainAuth("", ep.User, ep.Password, host)); err != nil {
		return "", fmt.Errorf("sign in to the email service: %w", err)
	}
	if err := cl.Mail(m.From); err != nil {
		return "", err
	}
	for _, rcpt := range append(append(append([]string{}, m.To...), m.Cc...), m.Bcc...) {
		if err := cl.Rcpt(rcpt); err != nil {
			return "", err
		}
	}
	w, err := cl.Data()
	if err != nil {
		return "", err
	}
	if _, err := w.Write(msg); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	_ = cl.Quit()
	// Mailpit stores the message as it accepts it; find its ID by the Message-ID we set.
	for range 20 {
		var res mpList
		q := url.Values{"query": {"message-id:" + strings.Trim(id, "<>")}}
		if err := c.get(ctx, ep, "/api/v1/search?"+q.Encode(), &res); err == nil && len(res.Messages) > 0 {
			return res.Messages[0].ID, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return strings.Trim(id, "<>"), nil
}

type mpAddress struct {
	Name    string `json:"Name"`
	Address string `json:"Address"`
}

type mpSummary struct {
	ID      string      `json:"ID"`
	From    *mpAddress  `json:"From"`
	To      []mpAddress `json:"To"`
	Cc      []mpAddress `json:"Cc"`
	Bcc     []mpAddress `json:"Bcc"`
	ReplyTo []mpAddress `json:"ReplyTo"`
	Subject string      `json:"Subject"`
	Created time.Time   `json:"Created"`
	Date    time.Time   `json:"Date"`
	Size    int64       `json:"Size"`
	Text    string      `json:"Text"`
	HTML    string      `json:"HTML"`
}

type mpList struct {
	Total    int         `json:"total"`
	Messages []mpSummary `json:"messages"`
}

func (c Mailpit) get(ctx context.Context, ep Endpoint, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(ep.APIURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(ep.User, ep.Password)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("the email service answered %s", res.Status)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}

func addresses(l []mpAddress) []string {
	out := make([]string, 0, len(l))
	for _, a := range l {
		out = append(out, a.Address)
	}
	return out
}

func (s mpSummary) summary(relaying bool) api.EmailSummary {
	e := api.EmailSummary{ID: s.ID, To: addresses(s.To), Subject: s.Subject, SentAt: s.Created, SizeBytes: s.Size, Relay: api.RelayCaptured}
	if s.From != nil {
		e.From = s.From.Address
	}
	if e.SentAt.IsZero() {
		e.SentAt = s.Date
	}
	if relaying {
		e.Relay = api.RelayRelayed
	}
	return e
}

// List implements Client.
func (c Mailpit) List(ctx context.Context, ep Endpoint, limit int) (api.EmailList, error) {
	var res mpList
	if err := c.get(ctx, ep, "/api/v1/messages?limit="+strconv.Itoa(limit), &res); err != nil {
		return api.EmailList{}, err
	}
	out := api.EmailList{Total: res.Total, Items: make([]api.EmailSummary, 0, len(res.Messages))}
	for _, m := range res.Messages {
		out.Items = append(out.Items, m.summary(ep.Relaying))
	}
	return out, nil
}

// Get implements Client.
func (c Mailpit) Get(ctx context.Context, ep Endpoint, id string) (api.Email, error) {
	if id == "" || strings.ContainsAny(id, "/?#%") {
		return api.Email{}, ErrNotFound
	}
	var m mpSummary
	if err := c.get(ctx, ep, "/api/v1/message/"+id, &m); err != nil {
		return api.Email{}, err
	}
	headers := map[string][]string{}
	if err := c.get(ctx, ep, "/api/v1/message/"+id+"/headers", &headers); err != nil && !errors.Is(err, ErrNotFound) {
		return api.Email{}, err
	}
	e := api.Email{EmailSummary: m.summary(ep.Relaying), Cc: addresses(m.Cc), Bcc: addresses(m.Bcc), Text: m.Text, HTML: m.HTML, Headers: headers}
	if len(m.ReplyTo) > 0 {
		e.ReplyTo = m.ReplyTo[0].Address
	}
	return e, nil
}
