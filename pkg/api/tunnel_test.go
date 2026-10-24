package api

import (
	"slices"
	"testing"
)

// Public access rules (L3-002 §16.5).

func TestTunnelValidation(t *testing.T) {
	ok := map[string]string{
		`{"provider":"quick","app":"web"}`:                                                                                                      TunnelQuick,
		`{"provider":"tailscale","credentialsSecret":"ts-key","app":"web"}`:                                                                     TunnelTailscale,
		`{"provider":"tailscale","credentialsSecret":"ts-key","app":"web","mode":"public","hostname":"Stage-1"}`:                                TunnelTailscale,
		`{"provider":"ngrok","credentialsSecret":"ngrok","app":"web","domain":"Stage.NGROK.app."}`:                                              TunnelNgrok,
		`{"provider":"custom","app":"web","image":"ghcr.io/acme/tunnel:1","command":["run","$ENCLAVE_UPSTREAM"],"url":"https://x.example.net"}`: TunnelCustom,
		`{"provider":"cloudflare","credentialsSecret":"cf-tunnel","accountId":"0123456789ABCDEF0123456789abcdef"}`:                              TunnelCloudflare,
		`{"provider":"relay","machine":"vps1"}`:                                                                                                 TunnelRelay,
		`{"provider":"quick","app":"web","port":8080,"gate":{"mode":"password","passwordSecret":"stage-pass","openPaths":["/webhooks/"]}}`:      TunnelQuick,
	}
	for spec, provider := range ok {
		s, errs := normalize(t, KindTunnel, spec)
		if len(errs) != 0 {
			t.Errorf("%s refused: %v", spec, errs)
			continue
		}
		if s.(TunnelSpec).Provider != provider {
			t.Errorf("%s: provider %q", spec, s.(TunnelSpec).Provider)
		}
	}
	s, _ := normalize(t, KindTunnel, `{"provider":"tailscale","credentialsSecret":"ts-key","app":"web","hostname":"Stage-1"}`)
	ts := s.(TunnelSpec)
	if ts.Mode != TailscalePrivate || ts.Hostname != "stage-1" || ts.GateOrDefault().Mode != GateNone {
		t.Errorf("tailscale defaults: %+v gate %+v", ts, ts.GateOrDefault())
	}
	if s, _ := normalize(t, KindTunnel, `{"provider":"tailscale","credentialsSecret":"ts-key","app":"web","mode":"public"}`); s.(TunnelSpec).GateOrDefault().Mode != GateSignin {
		t.Error("a public Tailscale link must default to the sign-in gate (GATE-01)")
	}
	if s, _ := normalize(t, KindTunnel, `{"provider":"quick","app":"web"}`); s.(TunnelSpec).GateOrDefault().Mode != GateSignin {
		t.Error("a quick link must default to the sign-in gate (GATE-01)")
	}
	if s, _ := normalize(t, KindTunnel, `{"provider":"ngrok","credentialsSecret":"ngrok","app":"web","domain":"Stage.NGROK.app."}`); s.(TunnelSpec).Domain != "stage.ngrok.app" {
		t.Errorf("ngrok domain not normalised: %+v", s)
	}

	cases := map[string]string{
		`{"provider":"godaddy"}`: "spec.provider:NOT_ALLOWED",
		`{"provider":"quick"}`:   "spec.app:REQUIRED",
		`{"provider":"quick","app":"web","credentialsSecret":"x-key"}`:                     "spec.credentialsSecret:NOT_ALLOWED",
		`{"provider":"quick","app":"web","port":70000}`:                                    "spec.port:OUT_OF_RANGE",
		`{"provider":"tailscale","app":"web"}`:                                             "spec.credentialsSecret:REQUIRED",
		`{"provider":"tailscale","credentialsSecret":"ts","app":"web","mode":"x"}`:         "spec.mode:NOT_ALLOWED",
		`{"provider":"tailscale","credentialsSecret":"tsk","app":"web","hostname":"a_b"}`:  "spec.hostname:INVALID_FORMAT",
		`{"provider":"quick","app":"web","mode":"public"}`:                                 "spec.mode:NOT_ALLOWED",
		`{"provider":"quick","app":"web","domain":"x.ngrok.app"}`:                          "spec.domain:NOT_ALLOWED",
		`{"provider":"cloudflare"}`:                                                        "spec.credentialsSecret:REQUIRED",
		`{"provider":"cloudflare","credentialsSecret":"cf-tunnel","app":"web"}`:            "spec.app:NOT_ALLOWED",
		`{"provider":"cloudflare","credentialsSecret":"cf-tunnel","gate":{"mode":"none"}}`: "spec.gate:NOT_ALLOWED",
		`{"provider":"cloudflare","credentialsSecret":"cf-tunnel","accountId":"acct"}`:     "spec.accountId:INVALID_FORMAT",
		`{"provider":"quick","app":"web","accountId":"0123456789abcdef0123456789abcdef"}`:  "spec.accountId:NOT_ALLOWED",
		`{"provider":"relay"}`:                              "spec.machine:REQUIRED",
		`{"provider":"quick","app":"web","machine":"vps1"}`: "spec.machine:NOT_ALLOWED",
		`{"provider":"custom","app":"web"}`:                 "spec.image:REQUIRED",
		`{"provider":"custom","app":"web","image":"x/y:1","env":{"ENCLAVE_UPSTREAM":"http://evil"}}`:                                     "spec.env.ENCLAVE_UPSTREAM:NOT_ALLOWED",
		`{"provider":"custom","app":"web","image":"x/y:1","url":"http://x.example"}`:                                                     "spec.url:INVALID_FORMAT",
		`{"provider":"quick","app":"web","image":"x/y:1"}`:                                                                               "spec.image:NOT_ALLOWED",
		`{"provider":"quick","app":"web","gate":{"mode":"open"}}`:                                                                        "spec.gate.mode:NOT_ALLOWED",
		`{"provider":"quick","app":"web","gate":{"mode":"password"}}`:                                                                    "spec.gate.passwordSecret:REQUIRED",
		`{"provider":"quick","app":"web","gate":{"mode":"signin","passwordSecret":"pw-secret"}}`:                                         "spec.gate.passwordSecret:NOT_ALLOWED",
		`{"provider":"quick","app":"web","gate":{"mode":"none","openPaths":["webhooks"]}}`:                                               "spec.gate.openPaths[0]:INVALID_FORMAT",
		`{"provider":"quick","app":"web","gate":{"mode":"none","openPaths":["/a/../b"]}}`:                                                "spec.gate.openPaths[0]:INVALID_FORMAT",
		`{"provider":"quick","app":"web","gate":{"mode":"none","openPaths":["/.enclave/gate/"]}}`:                                        "spec.gate.openPaths[0]:NOT_ALLOWED",
		`{"provider":"quick","app":"web","gate":{"mode":"none","openPaths":["/1","/2","/3","/4","/5","/6","/7","/8","/9","/10","/11"]}}`: "spec.gate.openPaths:TOO_LONG",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindTunnel, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
}

func TestTunnelImmutableFields(t *testing.T) {
	a := TunnelSpec{Provider: TunnelRelay, Machine: "vps1"}
	if errs := ImmutableViolations(KindTunnel, a, TunnelSpec{Provider: TunnelRelay, Machine: "vps2"}); len(errs) != 1 {
		t.Errorf("a relay's machine must be immutable: %v", errs)
	}
	q := TunnelSpec{Provider: TunnelQuick, App: "web"}
	if errs := ImmutableViolations(KindTunnel, q, TunnelSpec{Provider: TunnelQuick, App: "api"}); len(errs) != 0 {
		t.Errorf("an app link's app may change: %v", errs)
	}
	if errs := ImmutableViolations(KindTunnel, q, TunnelSpec{Provider: TunnelNgrok, App: "web"}); len(errs) != 1 {
		t.Errorf("the provider must be immutable: %v", errs)
	}
}

func TestPublicDomainValidation(t *testing.T) {
	s, errs := normalize(t, KindDomain, `{"hostname":"Stage.Example.com","app":"web","tunnel":"home","zone":"example","cloudflareAccess":{"emails":["Sunny@Example.com"],"emailDomains":["@example.com"]}}`)
	if len(errs) != 0 {
		t.Fatalf("public domain refused: %v", errs)
	}
	d := s.(DomainSpec)
	if !d.IsPublic() || d.TLS != TLSAuto || d.GateOrDefault().Mode != GateSignin || d.CloudflareAccess.Emails[0] != "sunny@example.com" || d.CloudflareAccess.EmailDomains[0] != "example.com" {
		t.Errorf("defaults or normalisation missing: %+v %+v", d, d.CloudflareAccess)
	}
	if s, _ := normalize(t, KindDomain, `{"hostname":"api.example.com","app":"web"}`); s.(DomainSpec).IsPublic() {
		t.Error("a domain without a tunnel is not public")
	}
	cases := map[string]string{
		`{"hostname":"a.example.com","app":"web","gate":{"mode":"none"}}`:                                 "spec.gate:NOT_ALLOWED",
		`{"hostname":"a.example.com","app":"web","cloudflareAccess":{"emails":["a@b.co"]}}`:               "spec.cloudflareAccess:NOT_ALLOWED",
		`{"hostname":"a.example.com","app":"web","tunnel":"home","tls":"internal"}`:                       "spec.tls:NOT_ALLOWED",
		`{"hostname":"a.localhost","app":"web","tunnel":"home"}`:                                          "spec.hostname:NOT_ALLOWED",
		`{"hostname":"a.example.com","app":"web","tunnel":"Home!"}`:                                       "spec.tunnel:INVALID_FORMAT",
		`{"hostname":"a.example.com","app":"web","tunnel":"home","gate":{"mode":"password"}}`:             "spec.gate.passwordSecret:REQUIRED",
		`{"hostname":"a.example.com","app":"web","tunnel":"home","cloudflareAccess":{}}`:                  "spec.cloudflareAccess:REQUIRED",
		`{"hostname":"a.example.com","app":"web","tunnel":"home","cloudflareAccess":{"emails":["nope"]}}`: "spec.cloudflareAccess.emails[0]:INVALID_FORMAT",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindDomain, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
}

func TestEdgeMachineValidation(t *testing.T) {
	if errs := fieldsOf(ValidateAddMachine(AddMachine{Name: "vps1", Role: MachineEdge, Address: "203.0.113.9"})); len(errs) != 0 {
		t.Errorf("edge machine refused: %v", errs)
	}
	if errs := fieldsOf(ValidateAddMachine(AddMachine{Name: "vps1", Role: MachineEdge, Address: "edge.example.com"})); len(errs) != 0 {
		t.Errorf("edge machine by host name refused: %v", errs)
	}
	cases := map[string]AddMachine{
		"address:INVALID_FORMAT": {Name: "vps1", Role: MachineEdge},
		"address:NOT_ALLOWED":    {Name: "box2", Address: "203.0.113.9"},
		"role:NOT_ALLOWED":       {Name: "box2", Role: "gateway"},
	}
	for want, m := range cases {
		if errs := fieldsOf(ValidateAddMachine(m)); !slices.Contains(errs, want) {
			t.Errorf("%+v: want %s, got %v", m, want, errs)
		}
	}
}
