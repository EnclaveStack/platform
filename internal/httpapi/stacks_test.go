package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/internal/testenv"
	"github.com/enclavestack/platform/pkg/api"
	"github.com/enclavestack/platform/pkg/client"
)

// The catalogue and stacks (L2-010 CAT-*, ADR-048).

func members(r api.Resource) map[string]string {
	out := map[string]string{}
	raw, _ := json.Marshal(r.Outputs[api.OutputMembers])
	var list []api.StackMember
	_ = json.Unmarshal(raw, &list)
	for _, m := range list {
		out[string(m.Kind)+"/"+m.Name] = m.ID
	}
	return out
}

func TestAWordPressStackFromTheCatalogue(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()

	// Six apps and six ways in from the internet (L2-014 TUN-15).
	entries, err := admin.Catalogue(ctx)
	byCategory := map[string]int{}
	for _, e := range entries {
		byCategory[e.Category]++
	}
	if err != nil || byCategory[api.CategoryApps] != 6 || byCategory[api.CategoryPublicAccess] != 6 {
		t.Fatalf("catalogue: %v %v", byCategory, err)
	}
	wp, err := admin.CatalogueEntry(ctx, "wordpress", "")
	if err != nil || !wp.BuiltIn || len(wp.Members) != 4 || !wp.Members[3].Optional {
		t.Fatalf("wordpress: %+v %v", wp, err)
	}

	st, err := admin.CreateResource(ctx, api.KindStack, "blog", api.StackSpec{Template: "wordpress", Parameters: map[string]any{"domain": "blog.localhost"}})
	if err != nil {
		t.Fatal(err)
	}
	st = testenv.WaitReady(t, admin, st.ID)
	got := members(st)
	for _, want := range []string{"database/blog-db", "volume/blog-content", "app/blog", "domain/blog-site"} {
		if got[want] == "" {
			t.Errorf("no member %s in %v", want, got)
		}
	}
	if st.Outputs[api.OutputReady] != float64(4) || st.Outputs[api.OutputTemplateVersion] != "1.0.0" {
		t.Errorf("outputs %v", st.Outputs)
	}
	if outs, _ := st.Outputs[api.OutputStackOutputs].(map[string]any); outs["url"] != "https://blog.localhost" {
		t.Errorf("template outputs %v", st.Outputs[api.OutputStackOutputs])
	}
	app, _ := admin.GetResource(ctx, got["app/blog"])
	if app.Stack == nil || *app.Stack != st.ID {
		t.Errorf("the app's stack: %v", app.Stack)
	}
	var spec api.AppSpec
	_ = json.Unmarshal(app.Spec, &spec)
	if spec.Env["WORDPRESS_DB_HOST"] != "es-database-blog-db" || spec.SecretEnv["WORDPRESS_DB_PASSWORD"] != "blog-db-password" || spec.Volumes[0].Volume != "blog-content" {
		t.Errorf("the app's spec: %+v", spec)
	}
	if list, err := admin.ListResources(ctx, client.ListOptions{Stack: "blog"}); err != nil || len(list.Items) != 4 {
		t.Errorf("?stack=blog: %d %v", len(list.Items), err)
	}

	// Members are the stack's: not changed or deleted alone; actions work (CAT-06).
	_, err = admin.UpdateResource(ctx, got["app/blog"], map[string]any{"memoryMiB": 1024}, nil)
	mustCode(t, err, api.CodePartOfStack, "changing a member")
	_, err = admin.DeleteResource(ctx, got["database/blog-db"], nil)
	mustCode(t, err, api.CodePartOfStack, "deleting a member")
	if _, err := admin.Action(ctx, got["app/blog"], api.ActionRestart); err != nil {
		t.Errorf("restarting a member: %v", err)
	}

	// Changing the stack changes its members: more memory, and no domain any more (CAT-06).
	if _, err := admin.UpdateResource(ctx, st.ID, map[string]any{"parameters": map[string]any{"memoryMiB": 768, "domain": nil}}, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "the stack after the change", func() bool {
		r, _ := admin.GetResource(ctx, st.ID)
		_, err := admin.GetResource(ctx, got["domain/blog-site"])
		a, _ := admin.GetResource(ctx, got["app/blog"])
		var s api.AppSpec
		_ = json.Unmarshal(a.Spec, &s)
		return r.Status.Phase == api.PhaseReady && client.Code(err) == api.CodeNotFound && s.MemoryMiB == 768
	})

	// A name that exists: refused, and nothing is created (CAT-09).
	if db, err := admin.CreateResource(ctx, api.KindDatabase, "taken-db", api.DatabaseSpec{}); err != nil {
		t.Fatal(err)
	} else {
		testenv.WaitReady(t, admin, db.ID)
	}
	_, err = admin.CreateResource(ctx, api.KindStack, "taken", api.StackSpec{Template: "wordpress"})
	mustCode(t, err, api.CodeNameTaken, "a member's name taken")
	if _, err := admin.FindResource(ctx, api.KindVolume, "taken-content"); client.Code(err) != api.CodeNotFound {
		t.Errorf("a member was created though the stack was refused: %v", err)
	}
	if _, err := admin.FindResource(ctx, api.KindStack, "taken"); client.Code(err) != api.CodeNotFound {
		t.Errorf("the refused stack exists: %v", err)
	}
	_, err = admin.CreateResource(ctx, api.KindStack, "ghosty", api.StackSpec{Template: "ghost"})
	mustCode(t, err, api.CodeValidationFailed, "a required parameter missing")
	_, err = admin.CreateResource(ctx, api.KindStack, "nope", api.StackSpec{Template: "no-such-thing"})
	mustCode(t, err, api.CodeValidationFailed, "an unknown template")

	// Deleting the stack deletes its members (CAT-07).
	if _, err := admin.DeleteResource(ctx, st.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "the stack and its members gone", func() bool {
		_, e1 := admin.GetResource(ctx, st.ID)
		_, e2 := admin.GetResource(ctx, got["database/blog-db"])
		_, e3 := admin.FindResource(ctx, api.KindSecret, "blog-db-password")
		return client.Code(e1) == api.CodeNotFound && client.Code(e2) == api.CodeNotFound && client.Code(e3) == api.CodeNotFound
	})
}

const workerTemplate = `
name: shop-worker
title: Shop worker
version: 1.0.0
parameters:
  - { name: apiKey, title: API key, type: secret, required: true }
resources:
  - { kind: database, name: db, spec: { engine: postgres } }
  - { kind: secret, name: url, value: "postgres://${ident:db}:${password:db}@${host:database/db}:5432/${ident:db}" }
  - kind: app
    name: ""
    spec:
      image: nginx:1.27
      secretEnv: { DATABASE_URL: "${name:url}", API_KEY: "${param:apiKey}" }
`

func TestOwnTemplatesAndSecretsWithPasswords(t *testing.T) {
	env := testenv.Start(t)
	admin := env.Setup(t)
	ctx := context.Background()

	// Only machine admins add templates; a broken one is refused with its line (CAT-03).
	viewer := viewerClient(t, env, admin)
	_, err := viewer.AddTemplate(ctx, workerTemplate)
	mustCode(t, err, api.CodeForbidden, "a viewer adding a template")
	_, err = admin.AddTemplate(ctx, strings.Replace(workerTemplate, "kind: app", "kind: rocket", 1))
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeValidationFailed || len(ae.Details) == 0 || !strings.HasPrefix(ae.Details[0].Field, "body.line 10:") {
		t.Errorf("a broken template: %v %+v", err, ae)
	}
	if _, err := admin.AddTemplate(ctx, strings.Replace(workerTemplate, "name: shop-worker", "name: wordpress", 1)); client.Code(err) != api.CodeNameTaken {
		t.Errorf("shadowing a built-in: %v", err)
	}
	e, err := admin.AddTemplate(ctx, workerTemplate)
	if err != nil || e.BuiltIn || e.Name != "shop-worker" {
		t.Fatalf("add: %+v %v", e, err)
	}
	_, err = admin.AddTemplate(ctx, workerTemplate)
	mustCode(t, err, api.CodeNameTaken, "the same version again")
	if e, err = admin.AddTemplate(ctx, strings.Replace(workerTemplate, "1.0.0", "1.1.0", 1)); err != nil || len(e.Versions) != 2 || e.Version != "1.1.0" {
		t.Fatalf("a new version: %+v %v", e, err)
	}

	// A stack from it: the secret parameter is a secret of the stack, and the URL holds the database's
	// managed password, which never appears anywhere else.
	_, err = admin.CreateResource(ctx, api.KindStack, "worker", api.StackSpec{Template: "shop-worker"})
	mustCode(t, err, api.CodeValidationFailed, "a required secret parameter missing")
	st, err := admin.CreateResource(ctx, api.KindStack, "worker", api.StackSpec{Template: "shop-worker", SecretParameters: map[string]string{"apiKey": "sk_live_1"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(st.Spec), "sk_live_1") {
		t.Error("the secret parameter is in the stack's spec")
	}
	testenv.WaitReady(t, admin, st.ID)
	key, err := admin.FindResource(ctx, api.KindSecret, "worker-apikey")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := admin.RevealSecret(ctx, key.ID); v != "sk_live_1" {
		t.Errorf("the API key secret: %q", v)
	}
	pwSec, _ := admin.FindResource(ctx, api.KindSecret, "worker-db-password")
	pw, _ := admin.RevealSecret(ctx, pwSec.ID)
	urlSec, _ := admin.FindResource(ctx, api.KindSecret, "worker-url")
	url, _ := admin.RevealSecret(ctx, urlSec.ID)
	if pw == "" || url != "postgres://worker_db:"+pw+"@es-database-worker-db:5432/worker_db" {
		t.Errorf("the URL %q, the password %q", url, pw)
	}

	// A template stacks use can't be removed.
	err = admin.RemoveTemplate(ctx, "shop-worker")
	mustCode(t, err, api.CodeResourceInUse, "removing a template in use")
	err = admin.RemoveTemplate(ctx, "wordpress")
	mustCode(t, err, api.CodeActionNotSupported, "removing a built-in")
	if _, err := admin.DeleteResource(ctx, st.ID, nil); err != nil {
		t.Fatal(err)
	}
	testenv.Eventually(t, 10*time.Second, "worker gone", func() bool {
		_, err := admin.GetResource(ctx, st.ID)
		return client.Code(err) == api.CodeNotFound
	})
	if err := admin.RemoveTemplate(ctx, "shop-worker"); err != nil {
		t.Errorf("removing an unused template: %v", err)
	}

	// Inline templates work without the catalogue.
	inline, err := admin.CreateResource(ctx, api.KindStack, "inline", api.StackSpec{TemplateBody: strings.Replace(workerTemplate, "name: shop-worker", "name: inline-worker", 1),
		SecretParameters: map[string]string{"apiKey": "k"}})
	if err != nil {
		t.Fatal(err)
	}
	testenv.WaitReady(t, admin, inline.ID)
}
