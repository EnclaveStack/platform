package route53

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/sigv4"
)

// fakeRoute53 implements the Route 53 API calls the provider makes, as documented in the Route 53 API
// reference (ListHostedZonesByName, GetHostedZone, ListResourceRecordSets, ChangeResourceRecordSets),
// and verifies each request's SigV4 signature by signing it again.
type fakeRoute53 struct {
	mu       sync.Mutex
	secret   string
	sets     map[string]rrset // "name|type", names with the trailing dot as Route 53 stores them
	throttle int
	changes  []string
}

func (f *fakeRoute53) fail(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0"?><ErrorResponse xmlns="%s"><Error><Type>Sender</Type><Code>%s</Code><Message>%s</Message></Error></ErrorResponse>`, xmlns, code, msg)
}

func (f *fakeRoute53) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	// Re-sign a copy with the expected secret: a different signature means a wrong key or a bad signer.
	check, _ := http.NewRequest(r.Method, "http://"+r.Host+r.URL.RequestURI(), nil)
	for _, h := range []string{"Content-Type"} {
		if v := r.Header.Get(h); v != "" {
			check.Header.Set(h, v)
		}
	}
	when, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	sigv4.Sign(check, body, "AKIDTEST", f.secret, signingRegion, "route53", when)
	if check.Header.Get("Authorization") != r.Header.Get("Authorization") {
		f.fail(w, http.StatusForbidden, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided.")
		return
	}
	if f.throttle > 0 {
		f.throttle--
		f.fail(w, http.StatusBadRequest, "Throttling", "Rate exceeded")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, apiVersion)
	switch {
	case r.Method == http.MethodGet && path == "/hostedzonesbyname":
		name := r.URL.Query().Get("dnsname")
		zones := ""
		if name == "example.com" || name == "aaa.com" {
			zones = `<HostedZone><Id>/hostedzone/Z0EXAMPLE</Id><Name>example.com.</Name></HostedZone>` // the first zone at or after the name
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><ListHostedZonesByNameResponse xmlns="%s"><HostedZones>%s</HostedZones><IsTruncated>false</IsTruncated><MaxItems>1</MaxItems></ListHostedZonesByNameResponse>`, xmlns, zones)
	case r.Method == http.MethodGet && path == "/hostedzone/Z0EXAMPLE":
		fmt.Fprintf(w, `<?xml version="1.0"?><GetHostedZoneResponse xmlns="%s"><HostedZone><Id>/hostedzone/Z0EXAMPLE</Id><Name>example.com.</Name></HostedZone><DelegationSet><NameServers><NameServer>ns-1.awsdns-01.org</NameServer><NameServer>ns-2.awsdns-02.com</NameServer></NameServers></DelegationSet></GetHostedZoneResponse>`, xmlns)
	case r.Method == http.MethodGet && path == "/hostedzone/Z0EXAMPLE/rrset":
		start := r.URL.Query().Get("name") + "|" + r.URL.Query().Get("type")
		keys := make([]string, 0, len(f.sets))
		for k := range f.sets {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		var out []rrset
		for _, k := range keys {
			if k >= start {
				out = append(out, f.sets[k])
				break
			}
		}
		b, _ := xml.Marshal(struct {
			XMLName xml.Name `xml:"ListResourceRecordSetsResponse"`
			Sets    []rrset  `xml:"ResourceRecordSets>ResourceRecordSet"`
		}{Sets: out})
		_, _ = w.Write(b)
	case r.Method == http.MethodPost && path == "/hostedzone/Z0EXAMPLE/rrset":
		var req changeRequest
		if err := xml.Unmarshal(body, &req); err != nil || req.Xmlns != xmlns {
			f.fail(w, http.StatusBadRequest, "InvalidInput", "malformed")
			return
		}
		for _, c := range req.Changes {
			key := strings.ReplaceAll(c.Set.Name, "*", `\052`) + "|" + c.Set.Type
			c.Set.Name = strings.ReplaceAll(c.Set.Name, "*", `\052`)
			switch c.Action {
			case "UPSERT":
				f.sets[key] = c.Set
			case "DELETE":
				cur, ok := f.sets[key]
				if !ok || cur.TTL != c.Set.TTL || !slices.Equal(cur.Values, c.Set.Values) {
					f.fail(w, http.StatusBadRequest, "InvalidChangeBatch", "Tried to delete resource record set but it was not found or values did not match")
					return
				}
				delete(f.sets, key)
			}
			f.changes = append(f.changes, c.Action+" "+key)
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><ChangeResourceRecordSetsResponse xmlns="%s"><ChangeInfo><Id>/change/C1</Id><Status>PENDING</Status></ChangeInfo></ChangeResourceRecordSetsResponse>`, xmlns)
	default:
		f.fail(w, http.StatusNotFound, "NoSuchHostedZone", "No hosted zone found")
	}
}

func provider(t *testing.T, f *fakeRoute53, secret, zone string) *Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := New(zone, []byte(`{"accessKeyId":"AKIDTEST","secretAccessKey":"`+secret+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	p.Endpoint = srv.URL
	return p
}

func TestRecordSetsLifecycle(t *testing.T) {
	f := &fakeRoute53{secret: "s3cret", sets: map[string]rrset{
		"zzz.example.com.|A": {Name: "zzz.example.com.", Type: "A", TTL: 60, Values: []string{"192.0.2.1"}},
	}}
	p := provider(t, f, "s3cret", "example.com")
	ctx := context.Background()
	z, err := p.Zone(ctx)
	if err != nil || z.ID != "Z0EXAMPLE" || len(z.NameServers) != 2 {
		t.Fatalf("zone %+v %v", z, err)
	}
	// The listing starts at the name: a missing record must not be confused with the next one.
	if r, err := p.Get(ctx, "api.example.com", "A"); err != nil || r != nil {
		t.Fatalf("missing record: %+v %v", r, err)
	}
	if err := p.Set(ctx, dns.Record{FQDN: "api.example.com", Type: "A", Values: []string{"203.0.113.8", "203.0.113.7"}, TTL: 300}, "rec_1"); err != nil {
		t.Fatal(err)
	}
	r, err := p.Get(ctx, "api.example.com", "A")
	if err != nil || r == nil || !slices.Equal(r.Values, []string{"203.0.113.7", "203.0.113.8"}) || r.TTL != 300 || r.Owner != "" {
		t.Fatalf("after upsert: %+v %v", r, err)
	}
	// TXT values are quoted on the wire and come back unquoted; wildcards are escaped by Route 53.
	if err := p.Set(ctx, dns.Record{FQDN: "*.dev.example.com", Type: "TXT", Values: []string{"v=spf1 -all"}, TTL: 60}, "rec_2"); err != nil {
		t.Fatal(err)
	}
	if got := f.sets[`\052.dev.example.com.|TXT`]; len(got.Values) != 1 || got.Values[0] != `"v=spf1 -all"` {
		t.Errorf("TXT on the wire: %+v", got)
	}
	if r, _ := p.Get(ctx, "*.dev.example.com", "TXT"); r == nil || r.Values[0] != "v=spf1 -all" {
		t.Errorf("wildcard TXT read back: %+v", r)
	}
	// Delete sends the exact set (Route 53 refuses anything else), and deleting nothing is fine.
	if err := p.Delete(ctx, "api.example.com", "A"); err != nil {
		t.Fatal(err)
	}
	if err := p.Delete(ctx, "api.example.com", "A"); err != nil {
		t.Errorf("deleting nothing: %v", err)
	}
	if !slices.Contains(f.changes, "DELETE api.example.com.|A") {
		t.Errorf("changes %v", f.changes)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	ctx := context.Background()
	f := &fakeRoute53{secret: "s3cret", sets: map[string]rrset{}}
	if _, err := provider(t, f, "wrong", "example.com").Zone(ctx); !errors.Is(err, dns.ErrPermission) || !dns.Permanent(err) {
		t.Errorf("wrong key: %v", err)
	}
	if _, err := provider(t, f, "s3cret", "other.org").Zone(ctx); !errors.Is(err, dns.ErrNoZone) {
		t.Errorf("unknown zone: %v", err)
	}
	if _, err := provider(t, f, "s3cret", "aaa.com").Zone(ctx); !errors.Is(err, dns.ErrNoZone) {
		t.Errorf("the next zone by name was taken for this one: %v", err)
	}
	f.throttle = 1
	var rl *dns.RateLimitedError
	if _, err := provider(t, f, "s3cret", "example.com").Zone(ctx); !errors.As(err, &rl) || dns.Permanent(err) {
		t.Errorf("throttling: %v", err)
	}
	if _, err := New("example.com", []byte(`{"accessKeyId":"x"}`)); !errors.Is(err, dns.ErrPermission) {
		t.Errorf("incomplete credentials: %v", err)
	}
}
