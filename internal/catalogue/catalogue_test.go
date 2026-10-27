package catalogue

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/enclavestack/platform/pkg/api"
)

// Every built-in renders, and every member it renders is a valid API spec (CAT-02, CAT-08).
func TestEveryBuiltinRendersValidSpecs(t *testing.T) {
	if len(Builtins()) != 18 {
		t.Fatalf("%d built-ins", len(Builtins()))
	}
	// What an installer would give the Public access entries' required parameters (L2-014 TUN-15).
	given := map[string]any{"app": "web", "machine": "vps1", "image": "ghcr.io/acme/tunnel:1", "accountId": "0123456789abcdef0123456789abcdef",
		"projectId": "my-app-1a2b3", "clientId": "abc123def456"}
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
	if strings.Join(names, ",") != "auth0,clerk,cloudflare-tunnel,custom-tunnel,firebase,ghost,gitea,n8n,neon,ngrok,own-server,plausible,quick-link,sentry,supabase,tailscale,uptime-kuma,wordpress" {
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
		case api.CategoryApps, api.CategoryConnections:
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

// Supabase is a connection: its address and keys as secrets, and outputs naming the secret behind each
// variable, without the keys left empty (L2-015 CNX-02 – CNX-05).
func TestSupabaseConnection(t *testing.T) {
	tpl, ok := Builtin("supabase")
	if !ok {
		t.Fatal("no supabase entry")
	}
	if e := tpl.Entry(true, []string{tpl.Version}); e.Category != api.CategoryConnections || !strings.HasPrefix(e.Signup, "https://") {
		t.Fatalf("entry %+v", e)
	}
	for _, m := range tpl.Members {
		if m.Kind != api.KindSecret {
			t.Errorf("a connection runs nothing, but has a %s", m.Kind)
		}
	}
	if _, err := tpl.Render("supa", "shop", map[string]any{"project": "abcd.supabase.co"}, nil); err == nil || !strings.Contains(err.Error(), "anonKey") {
		t.Errorf("the anon key is required: %v", err)
	}
	if _, err := tpl.Render("supa", "shop", map[string]any{"project": "https://abcd.supabase.co"}, map[string]string{"anonKey": "a"}); err == nil {
		t.Error("the project address is a host name")
	}

	r, err := tpl.Render("supa", "shop", map[string]any{"project": "abcd.supabase.co"}, map[string]string{"anonKey": "anon-value"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Members) != 1 || r.Members[0].Name != "supa-url" || r.Members[0].Value != "https://abcd.supabase.co" {
		t.Errorf("members %+v", r.Members)
	}
	if len(r.SecretParams) != 1 || r.SecretParams["supa-anonkey"] != "anon-value" {
		t.Errorf("secrets %v", r.SecretParams)
	}
	want := map[string]string{"SUPABASE_URL": "supa-url", "SUPABASE_ANON_KEY": "supa-anonkey"}
	if fmt.Sprint(r.Outputs) != fmt.Sprint(want) {
		t.Errorf("outputs %v, want %v: keys not given are left out", r.Outputs, want)
	}

	r, err = tpl.Render("supa", "shop", map[string]any{"project": "abcd.supabase.co"},
		map[string]string{"anonKey": "a", "serviceKey": "s", "dbUrl": "postgresql://u:p@pooler.supabase.com:6543/postgres"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Outputs["SUPABASE_SERVICE_ROLE_KEY"] != "supa-servicekey" || r.Outputs["DATABASE_URL"] != "supa-dburl" || len(r.SecretParams) != 3 {
		t.Errorf("outputs %v, secrets %v", r.Outputs, r.SecretParams)
	}
	for _, v := range r.Outputs {
		if v == "a" || v == "s" || strings.Contains(v, "u:p@") {
			t.Errorf("an output shows a secret's value: %v", r.Outputs)
		}
	}
}

// Any template's output that uses an empty secret parameter is left out; others still render (CNX-05).
func TestOutputsSkipEmptySecretParameters(t *testing.T) {
	tpl, err := Parse([]byte(`
name: own
title: Own
version: 1.0.0
parameters:
  - { name: token, type: secret }
  - { name: host, type: string, default: "" }
resources: [{ kind: secret, name: a, generate: 16 }]
outputs:
  token: "${param:token}"
  host: "${param:host}"
  both: "${name:a} and ${param:token}"
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := tpl.Render("s", "default", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Outputs["host"]; !ok || len(r.Outputs) != 1 {
		t.Errorf("outputs %v: only host, which is no secret, stays when empty", r.Outputs)
	}
	// A secret kept from an earlier version of the stack counts as given.
	if r, _ := tpl.Render("s", "default", nil, map[string]string{"token": "kept"}); r.Outputs["both"] != "s-a and s-token" {
		t.Errorf("outputs %v", r.Outputs)
	}
}

// The connections of L2-016 CNX-08 – CNX-12: each runs nothing, needs its required settings, and its outputs
// name only the secrets that exist.
func TestConnectionTemplates(t *testing.T) {
	cases := []struct {
		name    string
		values  map[string]any
		secrets map[string]string
		want    map[string]string
		missing string // a required setting left out
	}{
		{name: "neon", secrets: map[string]string{"pooledUrl": "postgresql://u:p@ep-x-pooler.neon.tech/db"},
			want: map[string]string{"DATABASE_URL": "c-pooledurl"}, missing: "pooledUrl"},
		{name: "firebase", values: map[string]any{"projectId": "my-app-1a2b3"}, secrets: map[string]string{"serviceAccount": `{"type":"service_account"}`},
			want: map[string]string{"FIREBASE_PROJECT_ID": "c-projectid", "FIREBASE_AUTH_DOMAIN": "c-authdomain", "FIREBASE_SERVICE_ACCOUNT": "c-serviceaccount"}, missing: "projectId"},
		{name: "auth0", values: map[string]any{"domain": "acme.us.auth0.com", "clientId": "abc123def456"}, secrets: map[string]string{"clientSecret": "s3cret"},
			want: map[string]string{"AUTH0_DOMAIN": "c-domain", "AUTH0_ISSUER_BASE_URL": "c-issuer", "AUTH0_CLIENT_ID": "c-clientid", "AUTH0_CLIENT_SECRET": "c-clientsecret"}, missing: "clientId"},
		{name: "clerk", secrets: map[string]string{"secretKey": "sk_test_x", "publishableKey": "pk_test_y"},
			want: map[string]string{"CLERK_SECRET_KEY": "c-secretkey", "CLERK_PUBLISHABLE_KEY": "c-publishablekey"}, missing: "secretKey"},
		{name: "sentry", secrets: map[string]string{"dsn": "https://abc@o1.ingest.sentry.io/2"},
			want: map[string]string{"SENTRY_DSN": "c-dsn"}, missing: "dsn"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tpl, ok := Builtin(c.name)
			if !ok {
				t.Fatal("no entry")
			}
			if e := tpl.Entry(true, []string{tpl.Version}); e.Category != api.CategoryConnections || !strings.HasPrefix(e.Signup, "https://") {
				t.Fatalf("entry %+v", e)
			}
			for _, m := range tpl.Members {
				if m.Kind != api.KindSecret {
					t.Errorf("a connection runs nothing, but has a %s", m.Kind)
				}
			}
			r, err := tpl.Render("c", "shop", c.values, c.secrets)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(r.Outputs) != fmt.Sprint(c.want) {
				t.Errorf("outputs %v, want %v", r.Outputs, c.want)
			}
			for _, v := range r.Outputs {
				for _, secret := range c.secrets {
					if v == secret {
						t.Errorf("an output shows a secret's value: %v", r.Outputs)
					}
				}
			}
			values, secrets := maps.Clone(c.values), maps.Clone(c.secrets)
			delete(values, c.missing)
			delete(secrets, c.missing)
			if _, err := tpl.Render("c", "shop", values, secrets); err == nil || !strings.Contains(err.Error(), c.missing) {
				t.Errorf("without %s: %v", c.missing, err)
			}
		})
	}
	auth0, _ := Builtin("auth0")
	r, err := auth0.Render("c", "shop", map[string]any{"domain": "acme.us.auth0.com", "clientId": "abc123def456"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r.Members {
		if m.Name == "c-issuer" && m.Value != "https://acme.us.auth0.com" {
			t.Errorf("issuer %q", m.Value)
		}
	}
	// Only a required secret parameter lets a template have no resources.
	if _, err := Parse([]byte("name: conn\ntitle: X\nversion: 1.0.0\nparameters: [{ name: key, type: secret, required: true }]\nresources: []\noutputs: { KEY: \"${param:key}\" }\n")); err != nil {
		t.Errorf("a secret-only template: %v", err)
	}
	if _, err := Parse([]byte("name: conn\ntitle: X\nversion: 1.0.0\nparameters: [{ name: key, type: secret }]\nresources: []\n")); err == nil {
		t.Error("an optional secret alone can't make the stack's secret")
	}
}
