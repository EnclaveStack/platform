package cloudflare

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
)

// Fake is an in-memory stand-in for the parts of Cloudflare's API that tunnels use: tunnels, their
// routing rules and tokens, Access applications and zones. Tests and the fake engine's demos use it; it
// checks the token and the account like Cloudflare does.
type Fake struct {
	// Token and Account are what a caller must send; "" accepts any (the demo's fake engine). Permissions
	// lists what the token may do: "tunnel", "access", "zone" (all three when nil).
	Token       string
	Account     string
	Permissions map[string]bool

	mu       sync.Mutex
	tunnels  map[string]*fakeTunnel
	apps     map[string]accessAppBody
	zones    map[string]string // zone name → account ID
	nextID   int
	calls    int
	failNext int
	server   *httptest.Server
}

type fakeTunnel struct {
	Tunnel
	ingress []IngressRule
	deleted bool
}

// NewFake returns a fake Cloudflare for one account and token, serving on a local address.
func NewFake(token, account string) *Fake {
	f := &Fake{Token: token, Account: account, tunnels: map[string]*fakeTunnel{}, apps: map[string]accessAppBody{}, zones: map[string]string{}}
	f.server = httptest.NewServer(f)
	return f
}

// URL is the fake's API base, to pass as the client's BaseURL.
func (f *Fake) URL() string { return f.server.URL }

// Close stops serving.
func (f *Fake) Close() { f.server.Close() }

// AddZone makes zone exist, owned by account.
func (f *Fake) AddZone(zone, account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.zones[zone] = account
}

// Allow sets what the token may do ("tunnel", "access", "zone").
func (f *Fake) Allow(perms ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Permissions = map[string]bool{}
	for _, p := range perms {
		f.Permissions[p] = true
	}
}

// FailNext makes the next n calls answer 500 (an outage).
func (f *Fake) FailNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = n
}

// Tunnels returns the live tunnels by name.
func (f *Fake) Tunnels() map[string]Tunnel {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]Tunnel{}
	for _, t := range f.tunnels {
		if !t.deleted {
			out[t.Name] = t.Tunnel
		}
	}
	return out
}

// IngressOf returns a tunnel's routing rules.
func (f *Fake) IngressOf(id string) []IngressRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tunnels[id]; ok {
		return append([]IngressRule(nil), t.ingress...)
	}
	return nil
}

// SetIngressOutside changes a tunnel's routing rules as someone would in Cloudflare's dashboard.
func (f *Fake) SetIngressOutside(id string, rules []IngressRule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tunnels[id]; ok {
		t.ingress = rules
	}
}

// DeleteOutside deletes a tunnel as someone would in Cloudflare's dashboard.
func (f *Fake) DeleteOutside(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tunnels[id]; ok {
		t.deleted = true
	}
}

// AccessApps returns the Access applications by domain.
func (f *Fake) AccessApps() map[string]AccessApp {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]AccessApp{}
	for id, a := range f.apps {
		app := AccessApp{ID: id, Domain: a.Domain}
		for _, p := range a.Policies {
			for _, inc := range p.Include {
				if e, ok := inc["email"].(map[string]any); ok {
					app.Emails = append(app.Emails, fmt.Sprint(e["email"]))
				}
				if e, ok := inc["email"].(map[string]string); ok {
					app.Emails = append(app.Emails, e["email"])
				}
				if d, ok := inc["email_domain"].(map[string]any); ok {
					app.EmailDomains = append(app.EmailDomains, fmt.Sprint(d["domain"]))
				}
				if d, ok := inc["email_domain"].(map[string]string); ok {
					app.EmailDomains = append(app.EmailDomains, d["domain"])
				}
			}
		}
		out[a.Domain] = app
	}
	return out
}

// Calls returns how many requests were answered.
func (f *Fake) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *Fake) may(perm string) bool { return f.Permissions == nil || f.Permissions[perm] }

func reply(w http.ResponseWriter, status int, result any, errs ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var apiErrs []apiError
	for _, e := range errs {
		apiErrs = append(apiErrs, apiError{Code: status * 10, Message: e})
	}
	raw, _ := json.Marshal(result)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": status < 300, "errors": apiErrs, "result": json.RawMessage(raw)})
}

// ServeHTTP implements the API.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failNext > 0 {
		f.failNext--
		reply(w, http.StatusInternalServerError, nil, "Internal server error")
		return
	}
	if got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); got == "" || (f.Token != "" && got != f.Token) {
		reply(w, http.StatusUnauthorized, nil, "Invalid API Token")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) >= 1 && parts[0] == "zones" {
		if !f.may("zone") {
			reply(w, http.StatusForbidden, nil, "Authentication error")
			return
		}
		name := r.URL.Query().Get("name")
		var out []map[string]any
		if acct, ok := f.zones[name]; ok {
			out = append(out, map[string]any{"id": "zone-" + name, "name": name, "account": map[string]string{"id": acct}})
		}
		reply(w, http.StatusOK, out)
		return
	}
	if len(parts) < 3 || parts[0] != "accounts" {
		reply(w, http.StatusNotFound, nil, "No route")
		return
	}
	if f.Account != "" && parts[1] != f.Account {
		reply(w, http.StatusForbidden, nil, "Authentication error")
		return
	}
	switch parts[2] {
	case "cfd_tunnel":
		if !f.may("tunnel") {
			reply(w, http.StatusForbidden, nil, "Authentication error")
			return
		}
		f.serveTunnels(w, r, parts[3:])
	case "access":
		if !f.may("access") {
			reply(w, http.StatusForbidden, nil, "Authentication error")
			return
		}
		f.serveAccess(w, r, parts[3:])
	default:
		reply(w, http.StatusNotFound, nil, "No route")
	}
}

func (f *Fake) serveTunnels(w http.ResponseWriter, r *http.Request, rest []string) {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			var out []Tunnel
			for _, t := range f.tunnels {
				if !t.deleted && (r.URL.Query().Get("name") == "" || t.Name == r.URL.Query().Get("name")) {
					out = append(out, t.Tunnel)
				}
			}
			reply(w, http.StatusOK, out)
		case http.MethodPost:
			var body struct {
				Name      string `json:"name"`
				ConfigSrc string `json:"config_src"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Name == "" || body.ConfigSrc != "cloudflare" {
				reply(w, http.StatusBadRequest, nil, "Bad tunnel")
				return
			}
			for _, t := range f.tunnels {
				if !t.deleted && t.Name == body.Name {
					reply(w, http.StatusConflict, nil, "A tunnel with that name already exists")
					return
				}
			}
			f.nextID++
			id := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.nextID)
			t := &fakeTunnel{Tunnel: Tunnel{ID: id, Name: body.Name, Status: "inactive"}}
			f.tunnels[id] = t
			reply(w, http.StatusOK, t.Tunnel)
		default:
			reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
		}
		return
	}
	t, ok := f.tunnels[rest[0]]
	if !ok || t.deleted {
		reply(w, http.StatusNotFound, nil, "Tunnel not found")
		return
	}
	switch {
	case len(rest) == 1 && r.Method == http.MethodGet:
		reply(w, http.StatusOK, t.Tunnel)
	case len(rest) == 1 && r.Method == http.MethodDelete:
		t.deleted = true
		reply(w, http.StatusOK, t.Tunnel)
	case len(rest) == 2 && rest[1] == "token" && r.Method == http.MethodGet:
		reply(w, http.StatusOK, "fake-run-token-"+t.ID)
	case len(rest) == 2 && rest[1] == "connections" && r.Method == http.MethodDelete:
		reply(w, http.StatusOK, nil)
	case len(rest) == 2 && rest[1] == "configurations" && r.Method == http.MethodGet:
		reply(w, http.StatusOK, map[string]any{"config": map[string]any{"ingress": t.ingress}})
	case len(rest) == 2 && rest[1] == "configurations" && r.Method == http.MethodPut:
		var body struct {
			Config struct {
				Ingress []IngressRule `json:"ingress"`
			} `json:"config"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			reply(w, http.StatusBadRequest, nil, "Bad configuration")
			return
		}
		rules := body.Config.Ingress
		if len(rules) == 0 || rules[len(rules)-1].Hostname != "" {
			reply(w, http.StatusBadRequest, nil, "The last ingress rule must match every hostname")
			return
		}
		t.ingress = rules
		reply(w, http.StatusOK, map[string]any{"tunnel_id": t.ID})
	default:
		reply(w, http.StatusNotFound, nil, "No route")
	}
}

func (f *Fake) serveAccess(w http.ResponseWriter, r *http.Request, rest []string) {
	if len(rest) == 0 || rest[0] != "apps" {
		reply(w, http.StatusNotFound, nil, "No route")
		return
	}
	if len(rest) == 1 {
		if r.Method != http.MethodPost {
			reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
			return
		}
		var body accessAppBody
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Domain == "" || body.Type != "self_hosted" {
			reply(w, http.StatusBadRequest, nil, "Bad application")
			return
		}
		f.nextID++
		id := fmt.Sprintf("app-%d", f.nextID)
		f.apps[id] = body
		reply(w, http.StatusOK, map[string]string{"id": id, "domain": body.Domain})
		return
	}
	id := rest[1]
	app, ok := f.apps[id]
	if !ok {
		reply(w, http.StatusNotFound, nil, "Application not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(w, http.StatusOK, map[string]any{"id": id, "domain": app.Domain, "policies": app.Policies})
	case http.MethodPut:
		var body accessAppBody
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			reply(w, http.StatusBadRequest, nil, "Bad application")
			return
		}
		f.apps[id] = body
		reply(w, http.StatusOK, map[string]string{"id": id})
	case http.MethodDelete:
		delete(f.apps, id)
		reply(w, http.StatusOK, map[string]string{"id": id})
	default:
		reply(w, http.StatusMethodNotAllowed, nil, "Method not allowed")
	}
}
