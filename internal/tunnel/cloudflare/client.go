// Package cloudflare manages Cloudflare tunnels and Access applications through the API v4 with the user's
// scoped token (L2-014 TUN-02, TUN-09, TUN-10, GATE-09; ADR-051). DNS records go through internal/dns.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is Cloudflare's API.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// CallTimeout bounds one call (L3-007 §14).
const CallTimeout = 15 * time.Second

// Error classes the reconciler maps to retry behaviour (TUN-09).
var (
	// ErrPermission: the token is rejected or lacks a permission; the message names it.
	ErrPermission = errors.New("the token was refused")
	// ErrInvalid: Cloudflare refused the request itself.
	ErrInvalid = errors.New("the request was refused")
	// ErrNotFound: what was asked for isn't there (a tunnel deleted at Cloudflare).
	ErrNotFound = errors.New("not found at Cloudflare")
)

// RateLimitedError asks to retry after a delay.
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("Cloudflare is rate limiting requests; retry in %s", e.RetryAfter.Round(time.Second))
}

// Permanent reports whether retrying err can't help.
func Permanent(err error) bool {
	return errors.Is(err, ErrPermission) || errors.Is(err, ErrInvalid)
}

// Credentials are what a Cloudflare tunnel's calls need: the API token (from its secret) and the account.
type Credentials struct {
	APIToken  string
	AccountID string
}

// Client talks to one Cloudflare account.
type Client struct {
	BaseURL  string
	APIToken string
	Account  string
	HTTP     *http.Client
}

// New returns a client for the account in creds, against base (DefaultBaseURL when "").
func New(base string, creds Credentials) *Client {
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), APIToken: creds.APIToken, Account: creds.AccountID, HTTP: &http.Client{Timeout: CallTimeout}}
}

// Tunnel is a remotely managed tunnel.
type Tunnel struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Status      string       `json:"status"`
	Connections []Connection `json:"connections"`
}

// Connection is one of a tunnel's connections to Cloudflare's edge.
type Connection struct {
	ColoName string `json:"colo_name"`
}

// IngressRule routes a hostname to a service; the last rule has no hostname.
type IngressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success bool            `json:"success"`
	Errors  []apiError      `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

// call does one request. need names the token permission the call uses, for the error message (TUN-09).
func (c *Client) call(ctx context.Context, method, path, need string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	cctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("cloudflare: %w", err) // network: transient
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("cloudflare: read response: %w", err)
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	msg := func() string {
		parts := make([]string, 0, len(env.Errors))
		for _, e := range env.Errors {
			parts = append(parts, fmt.Sprintf("%s (%d)", e.Message, e.Code))
		}
		if len(parts) == 0 {
			return resp.Status
		}
		return strings.Join(parts, "; ")
	}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return &RateLimitedError{RetryAfter: time.Duration(max(after, 1)) * time.Second}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: Cloudflare says %s. The token needs %s", ErrPermission, msg(), need)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: %s", ErrNotFound, msg())
	case resp.StatusCode >= 500:
		return fmt.Errorf("cloudflare: %s", msg())
	case resp.StatusCode >= 400 || !env.Success:
		return fmt.Errorf("%w: Cloudflare says %s", ErrInvalid, msg())
	}
	if out != nil && len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("cloudflare: unexpected response: %w", err)
		}
	}
	return nil
}

// Permissions the calls need, as Cloudflare's token page names them.
const (
	needTunnel = "Account › Cloudflare Tunnel › Edit"
	needAccess = "Account › Access: Apps and Policies › Edit"
	needZone   = "Zone › Zone › Read"
)

func (c *Client) account() string { return "/accounts/" + url.PathEscape(c.Account) }

// EnsureTunnel returns the remotely managed tunnel named name, making it when there is none. Adopting by
// name keeps a create idempotent across crashes (ADR-051).
func (c *Client) EnsureTunnel(ctx context.Context, name string) (Tunnel, error) {
	if t, err := c.FindTunnel(ctx, name); err != nil || t.ID != "" {
		return t, err
	}
	var t Tunnel
	err := c.call(ctx, http.MethodPost, c.account()+"/cfd_tunnel", needTunnel, map[string]string{"name": name, "config_src": "cloudflare"}, &t)
	return t, err
}

// FindTunnel returns the live tunnel named name, or a zero Tunnel.
func (c *Client) FindTunnel(ctx context.Context, name string) (Tunnel, error) {
	var list []Tunnel
	q := url.Values{"name": {name}, "is_deleted": {"false"}}
	if err := c.call(ctx, http.MethodGet, c.account()+"/cfd_tunnel?"+q.Encode(), needTunnel, nil, &list); err != nil {
		return Tunnel{}, err
	}
	for _, t := range list {
		if t.Name == name {
			return t, nil
		}
	}
	return Tunnel{}, nil
}

// GetTunnel returns a tunnel by ID, ErrNotFound when it was deleted at Cloudflare.
func (c *Client) GetTunnel(ctx context.Context, id string) (Tunnel, error) {
	var t Tunnel
	err := c.call(ctx, http.MethodGet, c.account()+"/cfd_tunnel/"+url.PathEscape(id), needTunnel, nil, &t)
	return t, err
}

// Token returns the token the connector runs with. It is fetched each time and never stored.
func (c *Client) Token(ctx context.Context, id string) (string, error) {
	var token string
	err := c.call(ctx, http.MethodGet, c.account()+"/cfd_tunnel/"+url.PathEscape(id)+"/token", needTunnel, nil, &token)
	return token, err
}

// Ingress returns a tunnel's routing rules.
func (c *Client) Ingress(ctx context.Context, id string) ([]IngressRule, error) {
	var out struct {
		Config struct {
			Ingress []IngressRule `json:"ingress"`
		} `json:"config"`
	}
	err := c.call(ctx, http.MethodGet, c.account()+"/cfd_tunnel/"+url.PathEscape(id)+"/configurations", needTunnel, nil, &out)
	return out.Config.Ingress, err
}

// SetIngress writes a tunnel's whole routing rules: one per hostname to service, then the fixed 404.
func (c *Client) SetIngress(ctx context.Context, id string, hostnames []string, service string) error {
	return c.call(ctx, http.MethodPut, c.account()+"/cfd_tunnel/"+url.PathEscape(id)+"/configurations", needTunnel,
		map[string]any{"config": map[string]any{"ingress": IngressFor(hostnames, service)}}, nil)
}

// IngressFor is the routing rules for hostnames, sorted, ending in the 404 rule.
func IngressFor(hostnames []string, service string) []IngressRule {
	hosts := slices.Clone(hostnames)
	slices.Sort(hosts)
	rules := make([]IngressRule, 0, len(hosts)+1)
	for _, h := range slices.Compact(hosts) {
		rules = append(rules, IngressRule{Hostname: h, Service: service})
	}
	return append(rules, IngressRule{Service: "http_status:404"})
}

// SameIngress compares routing rules.
func SameIngress(a, b []IngressRule) bool { return slices.Equal(a, b) }

// DeleteTunnel removes a tunnel and its connections; a tunnel already gone is not an error.
func (c *Client) DeleteTunnel(ctx context.Context, id string) error {
	base := c.account() + "/cfd_tunnel/" + url.PathEscape(id)
	if err := c.call(ctx, http.MethodDelete, base+"/connections", needTunnel, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := c.call(ctx, http.MethodDelete, base, needTunnel, nil, nil); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// ZoneAccount returns the account that owns a zone, "" when the token can't read zones.
func (c *Client) ZoneAccount(ctx context.Context, zone string) (string, error) {
	var zones []struct {
		Name    string `json:"name"`
		Account struct {
			ID string `json:"id"`
		} `json:"account"`
	}
	err := c.call(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(zone), needZone, nil, &zones)
	if errors.Is(err, ErrPermission) {
		return "", nil // the check is a courtesy; the CNAME's own write would fail with a clear message
	}
	if err != nil {
		return "", err
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, zone) {
			return z.Account.ID, nil
		}
	}
	return "", nil
}

// AccessApp is a self-hosted Access application allowing listed emails and email domains.
type AccessApp struct {
	ID     string
	Domain string
	Emails []string
	// EmailDomains are the domains whose addresses may pass.
	EmailDomains []string
}

type accessAppBody struct {
	Name            string         `json:"name"`
	Domain          string         `json:"domain"`
	Type            string         `json:"type"`
	SessionDuration string         `json:"session_duration"`
	Policies        []accessPolicy `json:"policies"`
}

type accessPolicy struct {
	Name     string           `json:"name"`
	Decision string           `json:"decision"`
	Include  []map[string]any `json:"include"`
}

func appBody(a AccessApp) accessAppBody {
	var include []map[string]any
	for _, e := range a.Emails {
		include = append(include, map[string]any{"email": map[string]string{"email": e}})
	}
	for _, d := range a.EmailDomains {
		include = append(include, map[string]any{"email_domain": map[string]string{"domain": d}})
	}
	return accessAppBody{Name: "enclave " + a.Domain, Domain: a.Domain, Type: "self_hosted", SessionDuration: "24h",
		Policies: []accessPolicy{{Name: "enclave allow", Decision: "allow", Include: include}}}
}

// SetAccessApp makes or updates the application for a.Domain; it returns the application's ID.
func (c *Client) SetAccessApp(ctx context.Context, a AccessApp) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	if a.ID != "" {
		err := c.call(ctx, http.MethodPut, c.account()+"/access/apps/"+url.PathEscape(a.ID), needAccess, appBody(a), &out)
		if !errors.Is(err, ErrNotFound) {
			return a.ID, err
		}
	}
	err := c.call(ctx, http.MethodPost, c.account()+"/access/apps", needAccess, appBody(a), &out)
	return out.ID, err
}

// GetAccessApp reads an application back, ErrNotFound when it was deleted at Cloudflare.
func (c *Client) GetAccessApp(ctx context.Context, id string) (AccessApp, error) {
	var out struct {
		ID       string         `json:"id"`
		Domain   string         `json:"domain"`
		Policies []accessPolicy `json:"policies"`
	}
	if err := c.call(ctx, http.MethodGet, c.account()+"/access/apps/"+url.PathEscape(id), needAccess, nil, &out); err != nil {
		return AccessApp{}, err
	}
	a := AccessApp{ID: out.ID, Domain: out.Domain}
	for _, p := range out.Policies {
		for _, inc := range p.Include {
			if e, ok := inc["email"].(map[string]any); ok {
				if s, ok := e["email"].(string); ok {
					a.Emails = append(a.Emails, s)
				}
			}
			if d, ok := inc["email_domain"].(map[string]any); ok {
				if s, ok := d["domain"].(string); ok {
					a.EmailDomains = append(a.EmailDomains, s)
				}
			}
		}
	}
	return a, nil
}

// DeleteAccessApp removes an application; one already gone is not an error.
func (c *Client) DeleteAccessApp(ctx context.Context, id string) error {
	err := c.call(ctx, http.MethodDelete, c.account()+"/access/apps/"+url.PathEscape(id), needAccess, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// SameAccess reports whether an application allows exactly the listed people.
func SameAccess(a AccessApp, emails, domains []string) bool {
	norm := func(s []string) []string {
		out := slices.Clone(s)
		for i := range out {
			out[i] = strings.ToLower(out[i])
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	return slices.Equal(norm(a.Emails), norm(emails)) && slices.Equal(norm(a.EmailDomains), norm(domains))
}

// TunnelTarget is the CNAME target of a tunnel's public hostnames.
func TunnelTarget(id string) string { return id + ".cfargotunnel.com" }
