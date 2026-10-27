package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// fields lists the fields of errs, joined, for compact assertions.
func fields(errs []FieldError) string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Field)
	}
	return strings.Join(out, ",")
}

func norm10(t *testing.T, k Kind, raw string) (any, []FieldError) {
	t.Helper()
	spec, errs, err := NormalizeSpec(k, json.RawMessage(raw))
	if err != nil {
		t.Fatalf("%s %s: %v", k, raw, err)
	}
	return spec, errs
}

// L2-016 FN-01, FN-02, FN-05.
func TestValidateFunction(t *testing.T) {
	spec, errs := norm10(t, KindFunction, `{"image":"alpine:3.22"}`)
	if len(errs) > 0 {
		t.Fatalf("a bare function: %v", errs)
	}
	f := spec.(FunctionSpec)
	if f.MemoryMiB != 256 || f.TimeoutSeconds != 60 || f.Concurrency != 5 {
		t.Errorf("defaults %+v", f)
	}
	_, errs = norm10(t, KindFunction, `{"image":"alpine:3.22","triggers":[{"schedule":"@daily","timeZone":"Europe/Berlin"},{"queue":"jobs"},{"bucket":"uploads","prefix":"in/"}]}`)
	if len(errs) > 0 {
		t.Errorf("three triggers: %v", errs)
	}
	cases := map[string]string{
		`{}`: "spec.image",
		`{"image":"alpine:3.22","timeoutSeconds":901}`:                                      "spec.timeoutSeconds",
		`{"image":"alpine:3.22","concurrency":21}`:                                          "spec.concurrency",
		`{"image":"alpine:3.22","memoryMiB":9000}`:                                          "spec.memoryMiB",
		`{"image":"alpine:3.22","env":{"ENCLAVE_RUN_ID":"x"}}`:                              "spec.env.ENCLAVE_RUN_ID",
		`{"image":"alpine:3.22","triggers":[{}]}`:                                           "spec.triggers[0]",
		`{"image":"alpine:3.22","triggers":[{"queue":"a","bucket":"b"}]}`:                   "spec.triggers[0]",
		`{"image":"alpine:3.22","triggers":[{"schedule":"@daily"},{"schedule":"@hourly"}]}`: "spec.triggers[1].schedule",
		`{"image":"alpine:3.22","triggers":[{"schedule":"every day"}]}`:                     "spec.triggers[0].schedule",
		`{"image":"alpine:3.22","triggers":[{"queue":"jobs","timeZone":"UTC"}]}`:            "spec.triggers[0].timeZone",
		`{"image":"alpine:3.22","triggers":[{"queue":"jobs"},{"queue":"jobs"}]}`:            "spec.triggers[1].queue",
		`{"image":"alpine:3.22","triggers":[{"bucket":"uploads","prefix":"../x/"}]}`:             "spec.triggers[0].prefix",
		`{"image":"alpine:3.22","triggers":[{"queue":"jobs","prefix":"a/"}]}`:               "spec.triggers[0].prefix",
		`{"image":"alpine:3.22","triggers":[{"bucket":"uploads"},{"bucket":"uploads"}]}`:              "spec.triggers[1]",
	}
	for raw, want := range cases {
		if _, errs := norm10(t, KindFunction, raw); !strings.Contains(fields(errs), want) {
			t.Errorf("%s: %v, want %s", raw, errs, want)
		}
	}
	if (FunctionSpec{Triggers: []FunctionTrigger{{Queue: "q"}, {Schedule: "@daily"}}}).Triggers[1].Schedule == "" {
		t.Fatal("setup")
	}
	if tr, ok := (FunctionSpec{Triggers: []FunctionTrigger{{Queue: "q"}, {Schedule: "@daily"}}}).Schedule(); !ok || tr.Schedule != "@daily" {
		t.Errorf("schedule %v %v", tr, ok)
	}
}

// A topic subscription targets exactly one of a queue, a URL or a function (FN-03).
func TestTopicFunctionSubscription(t *testing.T) {
	if _, errs := norm10(t, KindTopic, `{"subscriptions":[{"function":"resize"}]}`); len(errs) > 0 {
		t.Errorf("a function target: %v", errs)
	}
	if _, errs := norm10(t, KindTopic, `{"subscriptions":[{"function":"resize","queue":"q"}]}`); fields(errs) != "spec.subscriptions[0]" {
		t.Errorf("two targets: %v", errs)
	}
	if _, errs := norm10(t, KindTopic, `{"subscriptions":[{"function":"resize","secret":"s"}]}`); fields(errs) != "spec.subscriptions[0].secret" {
		t.Errorf("a signed function: %v", errs)
	}
	if (Subscription{Function: "resize"}).Target() != "function/resize" {
		t.Error("target")
	}
}

// PRV-01 – PRV-03.
func TestValidatePreviews(t *testing.T) {
	src := `"source":{"git":"https://github.com/acme/web.git","ref":"main","webhookSecret":"web-hook","deployOnPush":true}`
	spec, errs := norm10(t, KindApp, `{`+src+`,"previews":{"enabled":true,"domain":{"suffix":"Preview.Example.com."}}}`)
	if len(errs) > 0 {
		t.Fatalf("previews: %v", errs)
	}
	p := spec.(AppSpec).Previews
	if p.TTLHours != 72 || p.MaxActive != 5 || p.Domain.Suffix != "preview.example.com" || p.Domain.TLS != TLSInternal {
		t.Errorf("defaults %+v %+v", p, p.Domain)
	}
	cases := map[string]string{
		`{"image":"nginx:1.27","previews":{"enabled":true}}`:                                           "spec.previews",
		`{` + src + `,"previews":{"enabled":true,"ttlHours":721}}`:                                     "spec.previews.ttlHours",
		`{` + src + `,"previews":{"enabled":true,"maxActive":21}}`:                                     "spec.previews.maxActive",
		`{` + src + `,"previews":{"enabled":true,"domain":{"suffix":"*.x.com"}}}`:                      "spec.previews.domain.suffix",
		`{` + src + `,"previews":{"enabled":true,"domain":{"suffix":"p.x.com","tls":"magic"}}}`:        "spec.previews.domain.tls",
		`{"source":{"git":"https://github.com/acme/web.git","ref":"main","webhookSecret":"Bad Name"}}`: "spec.source.webhookSecret",
	}
	for raw, want := range cases {
		if _, errs := norm10(t, KindApp, raw); !strings.Contains(fields(errs), want) {
			t.Errorf("%s: %v, want %s", raw, errs, want)
		}
	}
}

func TestPreviewNames(t *testing.T) {
	if n := PreviewNameForPR("web", 42); n != "web-pr-42" {
		t.Errorf("%s", n)
	}
	long := strings.Repeat("a", 40)
	if n := PreviewNameForPR(long, 12345); len(n) > NameMaxLen || !strings.HasSuffix(n, "-pr-12345") || ValidateName(n) != nil {
		t.Errorf("%s (%d)", n, len(n))
	}
	if n := PreviewNameForPR("ab-"+strings.Repeat("c", 30)+"-x", 7); strings.Contains(n, "--") || ValidateName(n) != nil {
		t.Errorf("%s", n)
	}
	if n := PreviewNameForRef("web", "refs/heads/Feature/Login_Page"); n != "web-feature-login-page" {
		t.Errorf("%s", n)
	}
	if n := PreviewNameForRef("web", "###"); n != "web-preview" {
		t.Errorf("%s", n)
	}
	if PullRequestRef("github", 3) != "refs/pull/3/head" || PullRequestRef("gitlab", 3) != "refs/merge-requests/3/head" {
		t.Error("refs")
	}
}

// DBX-01 – DBX-05.
func TestValidateDatabaseOptions(t *testing.T) {
	if _, errs := norm10(t, KindDatabase, `{"tls":true,"exposed":true,"initScript":"CREATE TABLE t (id int);"}`); len(errs) > 0 {
		t.Errorf("postgres with options: %v", errs)
	}
	cases := map[string]string{
		`{"engine":"mongodb","version":"7.0","tls":true}`: "spec.tls",
		`{"exposed":true}`: "spec.exposed",
		`{"initScript":"` + strings.Repeat("x", InitScriptMax+1) + `"}`: "spec.initScript",
	}
	for raw, want := range cases {
		if _, errs := norm10(t, KindDatabase, raw); fields(errs) != want {
			t.Errorf("%.60s: %v, want %s", raw, errs, want)
		}
	}
	before := DatabaseSpec{Engine: EnginePostgres, Version: "16", InitScript: "a"}
	after := before
	after.InitScript = "b"
	if fields(ImmutableViolations(KindDatabase, before, after)) != "spec.initScript" {
		t.Error("the setup script can't change")
	}
	after = before
	after.TLS = true
	if len(ImmutableViolations(KindDatabase, before, after)) != 0 {
		t.Error("TLS can be turned on later")
	}
}

// VBK-01.
func TestValidateBackupVolume(t *testing.T) {
	spec, errs := norm10(t, KindBackup, `{"source":{"volume":"files"},"destination":{"bucket":"backups"}}`)
	if len(errs) > 0 {
		t.Fatalf("%v", errs)
	}
	if src := spec.(BackupSpec).Source; src.Kind() != KindVolume || src.Name() != "files" {
		t.Errorf("%+v", src)
	}
	if _, errs := norm10(t, KindBackup, `{"source":{"volume":"files","database":"db"},"destination":{"bucket":"backups"}}`); fields(errs) != "spec.source" {
		t.Errorf("%v", errs)
	}
}

// EVN-01 – EVN-03.
func TestValidateEventAlert(t *testing.T) {
	ok := `{"condition":{"type":"event","resources":["app/web"],"events":["ready","failed"]},"channels":["ops"]}`
	if _, errs := norm10(t, KindAlert, ok); len(errs) > 0 {
		t.Fatalf("%v", errs)
	}
	cases := map[string]string{
		`{"condition":{"type":"event","resources":["app/web"],"events":["exploded"]},"channels":["ops"]}`:                "spec.condition.events[0]",
		`{"condition":{"type":"event","resources":["app/web"],"events":[]},"channels":["ops"]}`:                          "spec.condition.events",
		`{"condition":{"type":"event","resources":["app/web"],"events":["ready","ready"]},"channels":["ops"]}`:           "spec.condition.events[1]",
		`{"condition":{"type":"event","resources":["app/web"],"events":["ready"]},"channels":["ops"],"repeatMinutes":5}`: "spec.repeatMinutes",
		`{"condition":{"type":"failed","resources":["app/web"],"events":["ready"]},"channels":["ops"]}`:                  "spec.condition.events",
	}
	for raw, want := range cases {
		if _, errs := norm10(t, KindAlert, raw); fields(errs) != want {
			t.Errorf("%s: %v, want %s", raw, errs, want)
		}
	}
}

// SBY-01: a standby is added like a worker.
func TestAddStandby(t *testing.T) {
	if errs := ValidateAddMachine(AddMachine{Name: "studio", Role: MachineStandby}); len(errs) > 0 {
		t.Errorf("%v", errs)
	}
	if errs := ValidateAddMachine(AddMachine{Name: "studio", Role: MachineStandby, Address: "1.2.3.4"}); fields(errs) != "address" {
		t.Errorf("%v", errs)
	}
}
