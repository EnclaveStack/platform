// Package cloudflare is the Cloudflare DNS provider (ADR-023): REST API v4 with a scoped API token.
// Cloudflare keeps one record per value; EnclaveStack writes the comment dns.OwnerComment on its records.
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

	"github.com/enclavestack/platform/internal/dns"
)

// DefaultBaseURL is Cloudflare's API.
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Credentials is the JSON of a zone's credentials secret.
type Credentials struct {
	APIToken string `json:"apiToken"`
}

// Provider talks to one zone.
type Provider struct {
	BaseURL  string
	Token    string
	ZoneName string
	HTTP     *http.Client

	zoneID string
}

// New builds a provider from the credentials secret's JSON.
func New(zone string, credentials []byte) (*Provider, error) {
	var c Credentials
	if err := json.Unmarshal(credentials, &c); err != nil || c.APIToken == "" {
		return nil, fmt.Errorf(`%w: the credentials secret must hold {"apiToken":"…"}`, dns.ErrPermission)
	}
	return &Provider{BaseURL: DefaultBaseURL, Token: c.APIToken, ZoneName: zone, HTTP: &http.Client{Timeout: dns.CallTimeout}}, nil
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope struct {
	Success    bool            `json:"success"`
	Errors     []apiError      `json:"errors"`
	Result     json.RawMessage `json:"result"`
	ResultInfo *struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
}

type record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Comment string `json:"comment,omitempty"`
}

func (p *Provider) call(ctx context.Context, method, path string, body any, out any) (*envelope, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	cctx, cancel := context.WithTimeout(ctx, dns.CallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, p.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cloudflare: %w", err) // network: transient
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("cloudflare: read response: %w", err)
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
		return nil, &dns.RateLimitedError{RetryAfter: time.Duration(max(after, 1)) * time.Second}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w: Cloudflare says %s. The token needs Zone:Read and DNS:Edit on %s", dns.ErrPermission, msg(), p.ZoneName)
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("cloudflare: %s", msg())
	case resp.StatusCode >= 400 || !env.Success:
		return nil, fmt.Errorf("%w: Cloudflare says %s", dns.ErrInvalid, msg())
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return nil, fmt.Errorf("cloudflare: unexpected response: %w", err)
		}
	}
	return &env, nil
}

// Zone implements dns.Provider.
func (p *Provider) Zone(ctx context.Context) (dns.Zone, error) {
	var zones []struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		NameServers []string `json:"name_servers"`
	}
	if _, err := p.call(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(p.ZoneName), nil, &zones); err != nil {
		return dns.Zone{}, err
	}
	for _, z := range zones {
		if strings.EqualFold(z.Name, p.ZoneName) {
			p.zoneID = z.ID
			return dns.Zone{ID: z.ID, NameServers: z.NameServers}, nil
		}
	}
	return dns.Zone{}, fmt.Errorf("%w: Cloudflare has no zone %s that this token can read", dns.ErrNoZone, p.ZoneName)
}

func (p *Provider) id(ctx context.Context) (string, error) {
	if p.zoneID != "" {
		return p.zoneID, nil
	}
	z, err := p.Zone(ctx)
	return z.ID, err
}

// list returns every Cloudflare record of a name and type (one per value), across pages.
func (p *Provider) list(ctx context.Context, fqdn, typ string) ([]record, error) {
	zid, err := p.id(ctx)
	if err != nil {
		return nil, err
	}
	var all []record
	for page := 1; ; page++ {
		var recs []record
		q := url.Values{"name": {fqdn}, "type": {typ}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		env, err := p.call(ctx, http.MethodGet, "/zones/"+zid+"/dns_records?"+q.Encode(), nil, &recs)
		if err != nil {
			return nil, err
		}
		all = append(all, recs...)
		if env.ResultInfo == nil || page >= env.ResultInfo.TotalPages {
			return all, nil
		}
	}
}

// Get implements dns.Provider.
func (p *Provider) Get(ctx context.Context, fqdn, typ string) (*dns.Record, error) {
	recs, err := p.list(ctx, fqdn, typ)
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	out := &dns.Record{FQDN: fqdn, Type: typ, TTL: recs[0].TTL}
	// The record is someone's only when every value carries the same owner comment.
	owner, same := "", true
	for i, r := range recs {
		out.Values = append(out.Values, r.Content)
		o, ok := strings.CutPrefix(r.Comment, dns.OwnerComment(""))
		if !ok || (i > 0 && o != owner) {
			same = false
		}
		owner = o
	}
	if same {
		out.Owner = owner
	}
	return out, nil
}

// Set implements dns.Provider: keep records whose value stays (updating TTL and comment), delete the
// others, create the missing ones.
func (p *Provider) Set(ctx context.Context, rec dns.Record, owner string) error {
	zid, err := p.id(ctx)
	if err != nil {
		return err
	}
	existing, err := p.list(ctx, rec.FQDN, rec.Type)
	if err != nil {
		return err
	}
	want := dns.Normalize(rec.Type, rec.Values)
	comment := dns.OwnerComment(owner)
	have := map[string]bool{}
	for _, e := range existing {
		v := dns.Normalize(rec.Type, []string{e.Content})[0]
		if !slices.Contains(want, v) || have[v] {
			if _, err := p.call(ctx, http.MethodDelete, "/zones/"+zid+"/dns_records/"+e.ID, nil, nil); err != nil {
				return err
			}
			continue
		}
		have[v] = true
		if e.TTL != rec.TTL || e.Comment != comment {
			body := record{Type: rec.Type, Name: rec.FQDN, Content: e.Content, TTL: rec.TTL, Comment: comment}
			if _, err := p.call(ctx, http.MethodPut, "/zones/"+zid+"/dns_records/"+e.ID, body, nil); err != nil {
				return err
			}
		}
	}
	for _, v := range want {
		if have[v] {
			continue
		}
		body := record{Type: rec.Type, Name: rec.FQDN, Content: v, TTL: rec.TTL, Comment: comment}
		if _, err := p.call(ctx, http.MethodPost, "/zones/"+zid+"/dns_records", body, nil); err != nil {
			return err
		}
	}
	return nil
}

// Delete implements dns.Provider.
func (p *Provider) Delete(ctx context.Context, fqdn, typ string) error {
	zid, err := p.id(ctx)
	if errors.Is(err, dns.ErrNoZone) {
		return nil
	}
	if err != nil {
		return err
	}
	recs, err := p.list(ctx, fqdn, typ)
	if err != nil {
		return err
	}
	for _, r := range recs {
		if _, err := p.call(ctx, http.MethodDelete, "/zones/"+zid+"/dns_records/"+r.ID, nil, nil); err != nil {
			return err
		}
	}
	return nil
}
