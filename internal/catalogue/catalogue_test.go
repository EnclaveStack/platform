package catalogue

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/enclavestack/platform/pkg/api"
)

// Every built-in renders, and every member it renders is a valid API spec (CAT-02, CAT-08).
func TestEveryBuiltinRendersValidSpecs(t *testing.T) {
	if len(Builtins()) != 12 {
		t.Fatalf("%d built-ins", len(Builtins()))
	}
	// What an installer would give the Public access entries' required parameters (L2-014 TUN-15).
	given := map[string]any{"app": "web", "machine": "vps1", "image": "ghcr.io/acme/tunnel:1", "accountId": "0123456789abcdef0123456789abcdef"}
	for _, tpl := range Builtins() {
		params := map[string]any{}
		secrets := map[string]string{}
		for _, p := range tpl.Parameters {
			switch {
			case p.Type == api.ParamHostname:
				params[p.Name] = tpl.Name + ".example.com"
			case p.Type == api.ParamSecret:
				secrets[p.Name] = "token-value"
			case p.Required:
				params[p.Name] = given[p.Name]
			}
		}
		r, err := tpl.Render("site", "shop", params, secrets)
		if err != nil {
			t.Errorf("%s: %v", tpl.Name, err)
			continue
		}
		for _, m := range r.Members {
			if m.Kind == api.KindSecret {
				if m.Generate == 0 && m.Value == "" {
					t.Errorf("%s: secret %s has neither", tpl.Name, m.Name)
				}
				continue
			}
			if _, errs, err := api.NormalizeSpec(m.Kind, m.Spec); err != nil || len(errs) > 0 {
				t.Errorf("%s: %s %s: %v %v\n%s", tpl.Name, m.Kind, m.Name, err, errs, m.Spec)
			}
		}
		if e := tpl.Entry(true, []string{tpl.Version}); len(e.Members) != len(tpl.Members) || !e.BuiltIn {
			t.Errorf("%s: entry %+v", tpl.Name, e)
		}
	}
}

const sample = `
name: shop-worker
title: Shop worker
version: 1.2.0
parameters:
  - { name: domain, type: hostname }
  - { name: replicas, type: number, default: 2 }
  - { name: apiKey, type: secret, required: true }
resources:
  - { kind: database, name: db, spec: { engine: postgres } }
  - { kind: secret, name: url, value: "postgres://${ident:db}:${password:db}@${host:database/db}/${ident:db}" }
  - kind: app
    name: ""
    spec:
      image: nginx:1.27
      port: 80
      replicas: "${param:replicas}"
      env: { STACK: "${stack} in ${project}", PRICE: "$$5" }
      secretEnv: { API_KEY: "${param:apiKey}", DB: "${name:url}" }
  - kind: domain
    name: site
    when: "${param:domain}"
    spec: { hostname: "${param:domain}", app: "${name:}" }
outputs:
  url: "https://${param:domain}"
`

func TestRenderFillsPlaceholdersWithTypes(t *testing.T) {
	tpl, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	r, err := tpl.Render("worker", "shop", map[string]any{"replicas": float64(3)}, map[string]string{"apiKey": "sk"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Members) != 3 {
		t.Fatalf("the domain should be left out without a domain: %+v", r.Members)
	}
	var app map[string]any
	_ = json.Unmarshal(r.Members[2].Spec, &app)
	if app["replicas"] != float64(3) {
		t.Errorf("replicas %#v: a whole-value placeholder takes the parameter's type", app["replicas"])
	}
	env := app["env"].(map[string]any)
	if env["STACK"] != "worker in shop" || env["PRICE"] != "$5" {
		t.Errorf("env %v", env)
	}
	sec := app["secretEnv"].(map[string]any)
	if sec["API_KEY"] != "worker-apikey" || sec["DB"] != "worker-url" || r.SecretParams["worker-apikey"] != "sk" {
		t.Errorf("secrets %v %v", sec, r.SecretParams)
	}
	url := r.Members[1].Value
	if !strings.Contains(url, "${password:db}") || !strings.Contains(url, "es-database-worker-db.shop") || !strings.HasPrefix(url, "postgres://worker_db:") {
		t.Errorf("secret value %q", url)
	}
	filled, err := FillPasswords(url, func(m string) (string, bool) { return "pw-" + m, m == "db" })
	if err != nil || !strings.Contains(filled, ":pw-db@") {
		t.Errorf("filled %q %v", filled, err)
	}
	if _, err := FillPasswords(url, func(string) (string, bool) { return "", false }); err == nil {
		t.Error("a missing password filled")
	}

	// With a domain, the domain is in.
	r, err = tpl.Render("worker", "default", map[string]any{"domain": "Shop.Example.com."}, map[string]string{"apiKey": "sk"})
	if err != nil || len(r.Members) != 4 || r.Outputs["url"] != "https://shop.example.com" {
		t.Fatalf("with a domain: %+v %v", r, err)
	}
	if !strings.Contains(r.Members[1].Value, "es-database-worker-db/") {
		t.Errorf("in default the host has no project: %q", r.Members[1].Value)
	}
}

func TestRenderRefusesBadParameters(t *testing.T) {
	tpl, _ := Parse([]byte(sample))
	cases := []struct {
		values  map[string]any
		secrets map[string]string
		want    string
	}{
		{map[string]any{}, nil, "secretParameters.apiKey"},
		{map[string]any{"replicas": "many"}, map[string]string{"apiKey": "k"}, "parameters.replicas"},
		{map[string]any{"domain": "not a host"}, map[string]string{"apiKey": "k"}, "parameters.domain"},
		{map[string]any{"colour": "red"}, map[string]string{"apiKey": "k"}, "parameters.colour"},
		{map[string]any{}, map[string]string{"apiKey": "k", "other": "x"}, "secretParameters.other"},
	}
	for _, c := range cases {
		_, err := tpl.Render("worker", "default", c.values, c.secrets)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v %v: %v (want %s)", c.values, c.secrets, err, c.want)
		}
	}
	long := strings.Repeat("a", 60)
	if _, err := tpl.Render(long, "default", nil, map[string]string{"apiKey": "k"}); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Errorf("a stack name too long for its members: %v", err)
	}
}

func TestParseFindsMistakesWithTheirLines(t *testing.T) {
	cases := map[string]string{
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: rocket, name: a, spec: {} }\n":                                    "line 5: resources[0].kind",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: app, name: a, spec: { image: \"${nope}\" } }\n":                   "not a placeholder",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: app, name: a, spec: { image: \"${param:img}\" } }\n":              "no parameter img",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: app, name: a, spec: { image: \"${password:a}\" } }\n":             "only goes into a secret",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: app, name: a, spec: {}, when: \"a ${stack}\" }\n":                 "when is one placeholder",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: app, name: a, spec: {} }\n  - { kind: app, name: a, spec: {} }\n": "share a name",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: secret, name: s }\n":                                              "generate (a random value) or value",
		"name: x\ntitle: X\nversion: 1\nresources:\n  - { kind: app, spec: {} }\n":                                                    "MAJOR.MINOR.PATCH",
		"name: x\ntitle: X\nversion: 1.0.0\nhooks: []\nresources:\n  - { kind: app, spec: {} }\n":                                     "line 4: hooks",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: stack, spec: {} }\n":                                              "a stack can hold",
		"name: x\ntitle: X\nversion: 1.0.0\nresources:\n  - { kind: app, spec: { image: \"${host:database/db}\" } }\n":                "no database named db",
		"not: [yaml": "isn't YAML",
		"name: x\ntitle: X\nversion: 1.0.0\nresources: []\n": "at least one resource",
	}
	for body, want := range cases {
		_, err := Parse([]byte(body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v (want %q)", body, err, want)
		}
	}
	if _, err := Parse([]byte(strings.Repeat(" ", api.TemplateMaxBytes+1))); err == nil {
		t.Error("an oversized template accepted")
	}
}

func TestALeftOutMemberCantBeUsed(t *testing.T) {
	tpl, err := Parse([]byte(`
name: left-out
title: X
version: 1.0.0
parameters: [{ name: cache, type: boolean }]
resources:
  - { kind: cache, name: c, when: "${param:cache}", spec: {} }
  - { kind: app, name: a, spec: { image: nginx, env: { REDIS: "${host:cache/c}" } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Render("s", "default", map[string]any{"cache": false}, nil); err == nil || !strings.Contains(err.Error(), "left out") {
		t.Errorf("got %v", err)
	}
	if r, err := tpl.Render("s", "default", map[string]any{"cache": true}, nil); err != nil || len(r.Members) != 2 {
		t.Errorf("with the cache: %+v %v", r, err)
	}
}

func TestGenerateMakesRandomText(t *testing.T) {
	a, b := Generate(48), Generate(48)
	if len(a) != 48 || a == b || strings.ContainsAny(a, "+/=") {
		t.Errorf("%q %q", a, b)
	}
}

// The catalogue lists its built-ins by name (L3-002 §11.5).
func TestBuiltinsAreByName(t *testing.T) {
	var names []string
	for _, b := range Builtins() {
		names = append(names, b.Name)
	}
	if strings.Join(names, ",") != "cloudflare-tunnel,custom-tunnel,ghost,gitea,n8n,ngrok,own-server,plausible,quick-link,tailscale,uptime-kuma,wordpress" {
		t.Errorf("%v", names)
	}
}

// The ways in are their own category, each saying what it needs (L2-014 TUN-15).
func TestPublicAccessCategory(t *testing.T) {
	var ways []string
	for _, b := range Builtins() {
		e := b.Entry(true, []string{b.Version})
		switch e.Category {
		case api.CategoryPublicAccess:
			ways = append(ways, e.Name)
			if len(e.Members) != 1 || e.Members[0].Kind != api.KindTunnel || e.Description == "" {
				t.Errorf("%s: %+v", e.Name, e)
			}
		case api.CategoryApps:
		default:
			t.Errorf("%s: category %q", e.Name, e.Category)
		}
	}
	if strings.Join(ways, ",") != "cloudflare-tunnel,custom-tunnel,ngrok,own-server,quick-link,tailscale" {
		t.Errorf("public access entries %v", ways)
	}
	if _, err := Parse([]byte("name: x-y\ntitle: X\nversion: 1.0.0\ncategory: games\nresources: [{kind: secret, name: a, generate: 16}]\n")); err == nil {
		t.Error("an unknown category must be refused")
	}
}
