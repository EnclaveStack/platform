package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/enclavestack/platform/internal/dns"
)

// fakeAPI implements the part of Cloudflare's API v4 the provider uses, as documented at
// developers.cloudflare.com/api (zones list; DNS records list, create, overwrite, delete), with
// paging, bearer-token auth and 429s.
type fakeAPI struct {
	mu      sync.Mutex
	token   string
	records map[string]record
	next    int
	limitN  int // answer the next N calls with 429
	perPage int
	calls   []string
}

func newFake() *fakeAPI { return &fakeAPI{token: "good", records: map[string]record{}, perPage: 100} }

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	reply := func(status int, result any, info any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		body := map[string]any{"success": status < 300, "errors": []any{}, "result": result}
		if status >= 300 {
			body["errors"] = []any{map[string]any{"code": 9109, "message": "Invalid access token"}}
		}
		if info != nil {
			body["result_info"] = info
		}
		_ = json.NewEncoder(w).Encode(body)
	}
	if f.limitN > 0 {
		f.limitN--
		w.Header().Set("Retry-After", "7")
		reply(http.StatusTooManyRequests, nil, nil)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		reply(http.StatusForbidden, nil, nil)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/zones":
		if r.URL.Query().Get("name") == "example.com" {
			reply(200, []any{map[string]any{"id": "z1", "name": "example.com", "name_servers": []string{"ada.ns.cloudflare.com", "bob.ns.cloudflare.com"}}}, nil)
		} else {
			reply(200, []any{}, nil)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/zones/z1/dns_records":
		q := r.URL.Query()
		var match []record
		for _, id := range sortedIDs(f.records) {
			rec := f.records[id]
			if rec.Name == q.Get("name") && rec.Type == q.Get("type") {
				match = append(match, rec)
			}
		}
		page, _ := strconv.Atoi(q.Get("page"))
		pages := max(1, (len(match)+f.perPage-1)/f.perPage)
		lo := min((page-1)*f.perPage, len(match))
		hi := min(lo+f.perPage, len(match))
		reply(200, match[lo:hi], map[string]int{"page": page, "total_pages": pages})
	case r.Method == http.MethodPost && r.URL.Path == "/zones/z1/dns_records":
		var rec record
		_ = json.NewDecoder(r.Body).Decode(&rec)
		f.next++
		rec.ID = "r" + strconv.Itoa(f.next)
		f.records[rec.ID] = rec
		reply(200, rec, nil)
	case strings.HasPrefix(r.URL.Path, "/zones/z1/dns_records/"):
		id := strings.TrimPrefix(r.URL.Path, "/zones/z1/dns_records/")
		if _, ok := f.records[id]; !ok {
			reply(404, nil, nil)
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.records, id)
			reply(200, map[string]string{"id": id}, nil)
			return
		}
		var rec record
		_ = json.NewDecoder(r.Body).Decode(&rec)
		rec.ID = id
		f.records[id] = rec
		reply(200, rec, nil)
	default:
		reply(404, nil, nil)
	}
}

func sortedIDs(m map[string]record) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			a, _ := strconv.Atoi(ids[i][1:])
			b, _ := strconv.Atoi(ids[j][1:])
			if b < a {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

func provider(t *testing.T, f *fakeAPI, token, zone string) *Provider {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	p, err := New(zone, []byte(`{"apiToken":"`+token+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	p.BaseURL = srv.URL
	return p
}

func TestZoneRecordsOwnershipAndPaging(t *testing.T) {
	f := newFake()
	f.perPage = 2
	p := provider(t, f, "good", "example.com")
	ctx := context.Background()
	z, err := p.Zone(ctx)
	if err != nil || z.ID != "z1" || len(z.NameServers) != 2 {
		t.Fatalf("zone %+v %v", z, err)
	}
	if r, err := p.Get(ctx, "api.example.com", "A"); err != nil || r != nil {
		t.Fatalf("missing record: %+v %v", r, err)
	}
	if err := p.Set(ctx, dns.Record{FQDN: "api.example.com", Type: "A", Values: []string{"203.0.113.7", "203.0.113.8", "203.0.113.9"}, TTL: 300}, "rec_1"); err != nil {
		t.Fatal(err)
	}
	r, err := p.Get(ctx, "api.example.com", "A")
	if err != nil || r == nil || !dns.SameValues("A", r.Values, []string{"203.0.113.9", "203.0.113.8", "203.0.113.7"}) || r.Owner != "rec_1" || r.TTL != 300 {
		t.Fatalf("after set (3 values over 2 pages): %+v %v", r, err)
	}
	// Changing values keeps the one that stays, deletes one, creates one; TTL changes update in place.
	f.calls = nil
	if err := p.Set(ctx, dns.Record{FQDN: "api.example.com", Type: "A", Values: []string{"203.0.113.7", "203.0.113.10"}, TTL: 60}, "rec_1"); err != nil {
		t.Fatal(err)
	}
	r, _ = p.Get(ctx, "api.example.com", "A")
	if !dns.SameValues("A", r.Values, []string{"203.0.113.7", "203.0.113.10"}) || r.TTL != 60 {
		t.Errorf("after change: %+v", r)
	}
	// A record someone else wrote has no owner.
	f.records["x"] = record{ID: "x", Type: "TXT", Name: "note.example.com", Content: `"hello"`, TTL: 1}
	r, _ = p.Get(ctx, "note.example.com", "TXT")
	if r == nil || r.Owner != "" || !dns.SameValues("TXT", r.Values, []string{"hello"}) {
		t.Errorf("foreign TXT %+v", r)
	}
	if err := p.Delete(ctx, "api.example.com", "A"); err != nil {
		t.Fatal(err)
	}
	if r, _ := p.Get(ctx, "api.example.com", "A"); r != nil {
		t.Errorf("not deleted: %+v", r)
	}
	if err := p.Delete(ctx, "api.example.com", "A"); err != nil {
		t.Errorf("deleting nothing: %v", err)
	}
}

func TestErrorsAreClassified(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	if _, err := provider(t, f, "bad", "example.com").Zone(ctx); !errors.Is(err, dns.ErrPermission) || !dns.Permanent(err) || !strings.Contains(err.Error(), "DNS:Edit") {
		t.Errorf("rejected token: %v", err)
	}
	if _, err := provider(t, f, "good", "other.org").Zone(ctx); !errors.Is(err, dns.ErrNoZone) {
		t.Errorf("unknown zone: %v", err)
	}
	f.limitN = 1
	var rl *dns.RateLimitedError
	if _, err := provider(t, f, "good", "example.com").Zone(ctx); !errors.As(err, &rl) || rl.RetryAfter.Seconds() != 7 || dns.Permanent(err) {
		t.Errorf("429: %v", err)
	}
	if _, err := New("example.com", []byte(`{}`)); !errors.Is(err, dns.ErrPermission) {
		t.Errorf("credentials without a token: %v", err)
	}
}
