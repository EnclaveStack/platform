// Package route53 is the Amazon Route 53 DNS provider (ADR-023): the REST API (2013-04-01), signed
// with SigV4 here rather than through an SDK. A record set holds all values of a name and type; Route 53
// stores no comments, so ownership is decided by the reconciler alone.
package route53

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/sigv4"
)

// DefaultEndpoint is Route 53's global API; it signs in us-east-1.
const (
	DefaultEndpoint = "https://route53.amazonaws.com"
	signingRegion   = "us-east-1"
	apiVersion      = "/2013-04-01"
	xmlns           = "https://route53.amazonaws.com/doc/2013-04-01/"
)

// Credentials is the JSON of a zone's credentials secret.
type Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
}

// Provider talks to one hosted zone.
type Provider struct {
	Endpoint string
	ZoneName string
	HTTP     *http.Client
	Now      func() time.Time

	creds  Credentials
	zoneID string
}

// New builds a provider from the credentials secret's JSON.
func New(zone string, credentials []byte) (*Provider, error) {
	var c Credentials
	if err := json.Unmarshal(credentials, &c); err != nil || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, fmt.Errorf(`%w: the credentials secret must hold {"accessKeyId":"…","secretAccessKey":"…"}`, dns.ErrPermission)
	}
	return &Provider{Endpoint: DefaultEndpoint, ZoneName: zone, HTTP: &http.Client{Timeout: dns.CallTimeout}, Now: time.Now, creds: c}, nil
}

type errorResponse struct {
	Error struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Error"`
}

func (p *Provider) call(ctx context.Context, method, path string, q url.Values, body []byte, out any) error {
	cctx, cancel := context.WithTimeout(ctx, dns.CallTimeout)
	defer cancel()
	u := p.Endpoint + apiVersion + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(cctx, method, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "text/xml")
	}
	sigv4.Sign(req, body, p.creds.AccessKeyID, p.creds.SecretAccessKey, signingRegion, "route53", p.Now())
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("route53: %w", err) // network: transient
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("route53: read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		var e errorResponse
		_ = xml.Unmarshal(raw, &e)
		msg := e.Error.Code + ": " + e.Error.Message
		switch {
		case e.Error.Code == "Throttling" || e.Error.Code == "PriorRequestNotComplete" || resp.StatusCode == http.StatusTooManyRequests:
			after, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
			return &dns.RateLimitedError{RetryAfter: time.Duration(max(after, 2)) * time.Second}
		case resp.StatusCode == http.StatusForbidden || e.Error.Code == "AccessDenied" || e.Error.Code == "InvalidClientTokenId" || e.Error.Code == "SignatureDoesNotMatch":
			return fmt.Errorf("%w: Route 53 says %s. The key needs route53:ListHostedZonesByName, GetHostedZone, ListResourceRecordSets and ChangeResourceRecordSets on %s",
				dns.ErrPermission, msg, p.ZoneName)
		case e.Error.Code == "NoSuchHostedZone":
			return fmt.Errorf("%w: %s", dns.ErrNoZone, msg)
		case resp.StatusCode >= 500:
			return fmt.Errorf("route53: %s", msg)
		}
		return fmt.Errorf("%w: Route 53 says %s", dns.ErrInvalid, msg)
	}
	if out != nil {
		if err := xml.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("route53: unexpected response: %w", err)
		}
	}
	return nil
}

// Zone implements dns.Provider.
func (p *Provider) Zone(ctx context.Context) (dns.Zone, error) {
	var list struct {
		HostedZones []struct {
			ID   string `xml:"Id"`
			Name string `xml:"Name"`
		} `xml:"HostedZones>HostedZone"`
	}
	if err := p.call(ctx, http.MethodGet, "/hostedzonesbyname", url.Values{"dnsname": {p.ZoneName}, "maxitems": {"1"}}, nil, &list); err != nil {
		return dns.Zone{}, err
	}
	if len(list.HostedZones) == 0 || !strings.EqualFold(strings.TrimSuffix(list.HostedZones[0].Name, "."), p.ZoneName) {
		return dns.Zone{}, fmt.Errorf("%w: Route 53 has no hosted zone %s for this key", dns.ErrNoZone, p.ZoneName)
	}
	id := strings.TrimPrefix(list.HostedZones[0].ID, "/hostedzone/")
	var zone struct {
		NameServers []string `xml:"DelegationSet>NameServers>NameServer"`
	}
	if err := p.call(ctx, http.MethodGet, "/hostedzone/"+id, nil, nil, &zone); err != nil {
		return dns.Zone{}, err
	}
	p.zoneID = id
	return dns.Zone{ID: id, NameServers: zone.NameServers}, nil
}

func (p *Provider) id(ctx context.Context) (string, error) {
	if p.zoneID != "" {
		return p.zoneID, nil
	}
	z, err := p.Zone(ctx)
	return z.ID, err
}

type rrset struct {
	Name   string   `xml:"Name"`
	Type   string   `xml:"Type"`
	TTL    int      `xml:"TTL"`
	Values []string `xml:"ResourceRecords>ResourceRecord>Value"`
}

// decodeName turns Route 53's escaped names ("\052.example.com.") into ours.
func decodeName(n string) string {
	return strings.TrimSuffix(strings.ReplaceAll(n, `\052`, "*"), ".")
}

// Get implements dns.Provider.
func (p *Provider) Get(ctx context.Context, fqdn, typ string) (*dns.Record, error) {
	id, err := p.id(ctx)
	if err != nil {
		return nil, err
	}
	var list struct {
		Sets []rrset `xml:"ResourceRecordSets>ResourceRecordSet"`
	}
	q := url.Values{"name": {fqdn + "."}, "type": {typ}, "maxitems": {"1"}}
	if err := p.call(ctx, http.MethodGet, "/hostedzone/"+id+"/rrset", q, nil, &list); err != nil {
		return nil, err
	}
	// The list starts at the name and type, and may begin with the next set when there is none.
	if len(list.Sets) == 0 || !strings.EqualFold(decodeName(list.Sets[0].Name), fqdn) || list.Sets[0].Type != typ {
		return nil, nil
	}
	s := list.Sets[0]
	return &dns.Record{FQDN: fqdn, Type: typ, TTL: s.TTL, Values: dns.Normalize(typ, s.Values)}, nil
}

type change struct {
	Action string `xml:"Action"`
	Set    rrset  `xml:"ResourceRecordSet"`
}

type changeRequest struct {
	XMLName xml.Name `xml:"ChangeResourceRecordSetsRequest"`
	Xmlns   string   `xml:"xmlns,attr"`
	Comment string   `xml:"ChangeBatch>Comment"`
	Changes []change `xml:"ChangeBatch>Changes>Change"`
}

func (p *Provider) change(ctx context.Context, comment string, changes ...change) error {
	id, err := p.id(ctx)
	if err != nil {
		return err
	}
	for i := range changes {
		changes[i].Set.Name += "."
		if changes[i].Set.Type == "TXT" {
			for j, v := range changes[i].Set.Values {
				changes[i].Set.Values[j] = strconv.Quote(v)
			}
		}
	}
	body, err := xml.Marshal(changeRequest{Xmlns: xmlns, Comment: comment, Changes: changes})
	if err != nil {
		return err
	}
	return p.call(ctx, http.MethodPost, "/hostedzone/"+id+"/rrset", nil, append([]byte(xml.Header), body...), nil)
}

// Set implements dns.Provider: one UPSERT of the whole record set.
func (p *Provider) Set(ctx context.Context, rec dns.Record, owner string) error {
	return p.change(ctx, dns.OwnerComment(owner), change{Action: "UPSERT",
		Set: rrset{Name: rec.FQDN, Type: rec.Type, TTL: rec.TTL, Values: dns.Normalize(rec.Type, rec.Values)}})
}

// Delete implements dns.Provider. Route 53 deletes only an exact record set, so it is read first.
func (p *Provider) Delete(ctx context.Context, fqdn, typ string) error {
	cur, err := p.Get(ctx, fqdn, typ)
	if errors.Is(err, dns.ErrNoZone) || (err == nil && cur == nil) {
		return nil
	}
	if err != nil {
		return err
	}
	return p.change(ctx, "removed by enclave", change{Action: "DELETE", Set: rrset{Name: fqdn, Type: typ, TTL: cur.TTL, Values: cur.Values}})
}
