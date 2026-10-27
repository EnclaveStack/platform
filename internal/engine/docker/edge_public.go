package docker

import (
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// The edge proxy's tunnel listeners (ADR-051 – ADR-053). Each tunnel gets servers of its own, bound to the
// edge's address on the network its traffic arrives on, routing exactly that tunnel's hostnames.

// Headers the edge sets for the gate (ADR-053).
const (
	headerEdgeKey    = "X-Enclave-Edge-Key"
	headerGateTarget = "X-Enclave-Gate-Target"
	headerClientIP   = "X-Enclave-Client-IP"
)

// publicServers renders the listeners' servers. addrs are the edge's addresses by network. It returns the
// hostnames whose certificates the edge obtains (relay routes without DNS-01), with the relay HTTP port
// that answers their challenges, and those it serves as loaded (DNS-01).
func publicServers(cfg engine.EdgeConfig, addrs map[string]string, proxy func(string) obj) (obj, map[int][]string, []string, error) {
	servers := obj{}
	acme := map[int][]string{}
	var loaded []string
	listeners := slices.Clone(cfg.Listeners)
	slices.SortFunc(listeners, func(a, b engine.Listener) int { return a.Port - b.Port })
	for _, l := range listeners {
		if l.Trusted == "" {
			// Its connector or relay hasn't started yet: nothing listens until the edge knows whom to believe.
			continue
		}
		bind := addrs[l.Network]
		if bind == "" {
			return nil, nil, nil, fmt.Errorf("the edge proxy isn't on the network %s that tunnel %s's traffic arrives on", l.Network, l.TunnelID)
		}
		routes := slices.Clone(l.Routes)
		sortRoutes(routes)
		var gated []obj
		for _, r := range routes {
			route := obj{"handle": gatedHandle(r, cfg.Gate, proxy), "terminal": true}
			if r.Hostname != "" {
				route["match"] = []obj{{"host": []string{r.Hostname}}}
			}
			gated = append(gated, route)
		}
		notFound := obj{"handle": []obj{{"handler": "static_response", "status_code": 404,
			"body": "No site is configured for this hostname on this EnclaveStack machine.\n"}}}
		name := "tunnel-" + strconv.Itoa(l.Port)
		trusted := []string{l.Trusted + "/32"}
		switch l.Kind {
		case engine.ListenerRelay:
			// TLS ends here (ADR-052): the relay passes encrypted bytes, with the visitor's address in front.
			var redirects []obj
			for _, r := range routes {
				if r.TLS {
					redirects = append(redirects, obj{"match": []obj{{"host": []string{r.Hostname}}}, "terminal": true, "handle": []obj{{"handler": "static_response",
						"status_code": 308, "headers": obj{"Location": []string{"https://{http.request.host}{http.request.uri}"}}}}})
					if r.DNS01 {
						loaded = append(loaded, r.Hostname)
					} else {
						acme[l.Port] = append(acme[l.Port], r.Hostname)
					}
				}
			}
			proxyProtocol := obj{"wrapper": "proxy_protocol", "allow": trusted}
			servers[name] = obj{"listen": []string{net.JoinHostPort(bind, strconv.Itoa(l.Port))}, "routes": append(redirects, notFound),
				"listener_wrappers": []obj{proxyProtocol}, "automatic_https": obj{"disable": true}}
			auto := obj{"disable_redirects": true}
			if hosts := hostsOf(routes, true); len(hosts) > 0 {
				var skip []string
				for _, h := range hosts {
					if slices.Contains(loaded, h) {
						skip = append(skip, h)
					}
				}
				if len(skip) > 0 {
					auto["skip_certificates"] = skip
				}
			}
			servers[name+"-tls"] = obj{"listen": []string{net.JoinHostPort(bind, strconv.Itoa(l.HTTPSPort))}, "routes": append(gated, notFound),
				"listener_wrappers": []obj{proxyProtocol, {"wrapper": "tls"}}, "automatic_https": auto}
		default:
			// TLS ends at the provider; the connector is the only source whose visitor address is believed.
			srv := obj{"listen": []string{net.JoinHostPort(bind, strconv.Itoa(l.Port))}, "routes": append(gated, notFound),
				"automatic_https": obj{"disable": true}, "trusted_proxies": obj{"source": "static", "ranges": trusted}}
			if l.ClientIPHeader != "" {
				srv["client_ip_headers"] = []string{l.ClientIPHeader}
				srv["trusted_proxies_strict"] = 1
			}
			servers[name] = srv
		}
	}
	return servers, acme, loaded, nil
}

// gateWait bounds the edge's calls to the gate (L3-007 §14).
const gateWait = "2s"

func gateTransport() obj {
	return obj{"protocol": "http", "dial_timeout": gateWait, "response_header_timeout": gateWait}
}

// sortRoutes puts exact hostnames first, then wildcards, then the catch-all of an app link.
func sortRoutes(routes []engine.GatedRoute) {
	rank := func(h string) int {
		switch {
		case h == "":
			return 2
		case strings.HasPrefix(h, "*."):
			return 1
		}
		return 0
	}
	slices.SortFunc(routes, func(a, b engine.GatedRoute) int {
		if ra, rb := rank(a.Hostname), rank(b.Hostname); ra != rb {
			return ra - rb
		}
		return strings.Compare(a.Hostname, b.Hostname)
	})
}

func hostsOf(routes []engine.GatedRoute, tlsOnly bool) []string {
	var out []string
	for _, r := range routes {
		if r.Hostname != "" && (!tlsOnly || r.TLS) {
			out = append(out, r.Hostname)
		}
	}
	return out
}

// gatedHandle is one public route: visitors' X-Enclave-* headers are dropped, open paths go straight to
// the app, then the gate decides (GATE-01 – GATE-07). The app always learns the request came over HTTPS.
func gatedHandle(r engine.GatedRoute, gate engine.GateConfig, proxy func(string) obj) []obj {
	app := proxy(r.Upstream)
	app["headers"] = obj{"request": obj{"set": obj{"X-Forwarded-Proto": []string{"https"}}}}
	var inner []obj
	if gate.Upstream != "" {
		// Git webhooks go to the gate listener before anything else, whatever the gate's mode (ADR-056).
		hooks := obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": gate.Upstream}}, "transport": gateTransport(),
			"headers": obj{"request": obj{"set": obj{headerEdgeKey: []string{gate.EdgeKey}}}}}
		inner = append(inner, obj{"match": []obj{{"path": []string{api.PublicHookPrefix + "*"}}}, "handle": []obj{hooks}, "terminal": true})
	}
	for _, p := range r.Gate.OpenPaths {
		inner = append(inner, obj{"match": []obj{{"path": []string{p + "*"}}}, "handle": []obj{app}, "terminal": true})
	}
	switch r.Gate.Mode {
	case api.GateNone:
		inner = append(inner, obj{"handle": []obj{app}})
	case api.GatePassword:
		auth := obj{"handler": "authentication", "providers": obj{"http_basic": obj{
			"accounts": []obj{{"username": r.PasswordUser, "password": r.PasswordHash}},
			"hash":     obj{"algorithm": "bcrypt"}, "hash_cache": obj{}, "realm": r.Hostname}}}
		inner = append(inner, obj{"handle": []obj{auth, app}})
	default: // signin, also what anything unknown falls back to: the gate fails closed
		// The visitor's address as the edge resolved it; visitors' own X-Enclave-* headers are already gone.
		gateHeaders := obj{"request": obj{"set": obj{headerEdgeKey: []string{gate.EdgeKey}, headerGateTarget: []string{r.Target}, headerClientIP: []string{"{http.vars.client_ip}"}}}}
		// The gate answers from the store at once; past gateWait the edge gives up and answers 502 or 504.
		pages := obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": gate.Upstream}}, "headers": gateHeaders, "transport": gateTransport()}
		check := obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": gate.Upstream}}, "transport": gateTransport(),
			"rewrite": obj{"method": "GET", "uri": "/check"},
			"headers": obj{"request": obj{"set": obj{headerEdgeKey: []string{gate.EdgeKey}, headerGateTarget: []string{r.Target}, headerClientIP: []string{"{http.vars.client_ip}"},
				"X-Forwarded-Method": []string{"{http.request.method}"}, "X-Forwarded-Uri": []string{"{http.request.uri}"}}}},
			"handle_response": []obj{{"match": obj{"status_code": []int{2}}, "routes": []obj{
				{"handle": []obj{{"handler": "vars"}}},
				{"match": []obj{{"not": []obj{{"vars": obj{"{http.reverse_proxy.header." + api.UserHeader + "}": []string{""}}}}}},
					"handle": []obj{{"handler": "headers", "request": obj{"set": obj{api.UserHeader: []string{"{http.reverse_proxy.header." + api.UserHeader + "}"}}}}}},
			}}}}
		inner = append(inner,
			obj{"match": []obj{{"path": []string{strings.TrimSuffix(api.GatePathPrefix, "/") + "/gate/*"}}}, "handle": []obj{pages}, "terminal": true},
			obj{"handle": []obj{check, app}})
	}
	return []obj{{"handler": "headers", "request": obj{"delete": []string{"X-Enclave-*"}}}, {"handler": "subroute", "routes": inner}}
}
