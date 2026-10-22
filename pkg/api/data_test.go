package api

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestDatabaseEngines(t *testing.T) {
	for engine, versions := range DatabaseEngineVersions {
		s, errs := normalize(t, KindDatabase, `{"engine":"`+engine+`"}`)
		if len(errs) != 0 || s.(DatabaseSpec).Version != DatabaseDefaultVersions[engine] {
			t.Errorf("%s defaults: %+v %v", engine, s, errs)
		}
		if !slices.Contains(versions, DatabaseDefaultVersions[engine]) {
			t.Errorf("%s's default version isn't one of its versions", engine)
		}
		for _, v := range versions {
			if _, errs := normalize(t, KindDatabase, `{"engine":"`+engine+`","version":"`+v+`"}`); len(errs) != 0 {
				t.Errorf("%s %s refused: %v", engine, v, errs)
			}
		}
	}
	if _, errs := normalize(t, KindDatabase, `{"engine":"mysql","version":"16"}`); !slices.Contains(errs, "spec.version:NOT_ALLOWED") {
		t.Errorf("mysql 16 accepted: %v", errs)
	}
	if v := ImmutableViolations(KindDatabase, DatabaseSpec{Engine: EnginePostgres, Version: "16"}, DatabaseSpec{Engine: EngineMySQL, Version: "8.4"}); len(v) != 2 {
		t.Errorf("engine change allowed: %v", v)
	}
	if len(DatabaseEngines) != len(DatabaseEngineVersions) {
		t.Error("every engine needs versions")
	}
}

func TestQueueValidation(t *testing.T) {
	s, errs := normalize(t, KindQueue, `{}`)
	q := s.(QueueSpec)
	if len(errs) != 0 || q.VisibilityTimeoutSeconds != 30 || q.RetentionHours != 96 || q.MaxMessageBytes != 256<<10 || q.MaxReceives != 0 {
		t.Errorf("defaults: %+v %v", q, errs)
	}
	cases := map[string]string{
		`{"visibilityTimeoutSeconds":43201}`:             "spec.visibilityTimeoutSeconds:OUT_OF_RANGE",
		`{"retentionHours":337}`:                         "spec.retentionHours:OUT_OF_RANGE",
		`{"maxMessageBytes":100}`:                        "spec.maxMessageBytes:OUT_OF_RANGE",
		`{"maxReceives":1001}`:                           "spec.maxReceives:OUT_OF_RANGE",
		`{"deadLetterQueue":"failed"}`:                   "spec.maxReceives:REQUIRED",
		`{"deadLetterQueue":"Bad_Name","maxReceives":3}`: "spec.deadLetterQueue:INVALID_FORMAT",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindQueue, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	if _, errs := normalize(t, KindQueue, `{"deadLetterQueue":"emails-failed","maxReceives":5}`); len(errs) != 0 {
		t.Errorf("dead letters refused: %v", errs)
	}
}

func TestTopicValidation(t *testing.T) {
	s, errs := normalize(t, KindTopic, `{"subscriptions":[{"queue":"emails"},{"url":" https://hooks.example.com/x ","secret":"hook-key"},{"url":"http://localhost:9000/h"}]}`)
	if len(errs) != 0 || s.(TopicSpec).RetentionHours != 24 || s.(TopicSpec).Subscriptions[1].URL != "https://hooks.example.com/x" {
		t.Fatalf("valid topic: %+v %v", s, errs)
	}
	cases := map[string]string{
		`{"subscriptions":[{}]}`: "spec.subscriptions[0]:EXACTLY_ONE",
		`{"subscriptions":[{"queue":"a-q","url":"https://x.example"}]}`: "spec.subscriptions[0]:EXACTLY_ONE",
		`{"subscriptions":[{"url":"http://example.com/h"}]}`:            "spec.subscriptions[0].url:INVALID_FORMAT",
		`{"subscriptions":[{"url":"https://u:p@example.com/h"}]}`:       "spec.subscriptions[0].url:INVALID_FORMAT",
		`{"subscriptions":[{"queue":"emails","secret":"k-ey"}]}`:        "spec.subscriptions[0].secret:NOT_ALLOWED",
		`{"subscriptions":[{"queue":"emails"},{"queue":"emails"}]}`:     "spec.subscriptions[1]:NOT_ALLOWED",
		`{"retentionHours":0.5}`:                                        "",
	}
	for spec, want := range cases {
		if want == "" {
			if _, _, err := NormalizeSpec(KindTopic, json.RawMessage(spec)); err == nil {
				t.Errorf("%s decoded", spec)
			}
			continue
		}
		if _, errs := normalize(t, KindTopic, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	subs := make([]Subscription, SubscriptionsMax+1)
	for i := range subs {
		subs[i] = Subscription{Queue: "q" + strings.Repeat("a", i+2)}
	}
	b, _ := json.Marshal(TopicSpec{Subscriptions: subs})
	if _, errs := normalize(t, KindTopic, string(b)); !slices.Contains(errs, "spec.subscriptions:TOO_LONG") {
		t.Errorf("21 subscriptions accepted: %v", errs)
	}
	if (Subscription{Queue: "emails"}).Target() != "queue/emails" {
		t.Error("queue target")
	}
}

func TestSearchValidation(t *testing.T) {
	s, errs := normalize(t, KindSearch, `{}`)
	if len(errs) != 0 || s.(SearchSpec).Version != "3" || s.(SearchSpec).MemoryMiB != 2048 {
		t.Errorf("defaults: %+v %v", s, errs)
	}
	for spec, want := range map[string]string{`{"version":"1"}`: "spec.version:NOT_ALLOWED", `{"memoryMiB":1000}`: "spec.memoryMiB:OUT_OF_RANGE", `{"memoryMiB":2100}`: "spec.memoryMiB:OUT_OF_RANGE"} {
		if _, errs := normalize(t, KindSearch, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	if v := ImmutableViolations(KindSearch, SearchSpec{Version: "2"}, SearchSpec{Version: "3"}); len(v) != 1 {
		t.Error("version change allowed")
	}
}

func TestTranscoderValidation(t *testing.T) {
	s, errs := normalize(t, KindTranscoder, `{"input":{"bucket":"uploads","prefix":"/videos"},"output":{"bucket":"uploads","prefix":"media"},"presets":["mp4-720p","thumbnail"]}`)
	tc := s.(TranscoderSpec)
	if len(errs) != 0 || tc.Input.Prefix != "videos/" || tc.Output.Prefix != "media/" || !tc.Watching() || tc.TimeoutSeconds != 3600 || tc.Concurrency != 1 || tc.MemoryMiB != 1024 {
		t.Fatalf("defaults: %+v %v", tc, errs)
	}
	if s, _ := normalize(t, KindTranscoder, `{"input":{"bucket":"a-in"},"output":{"bucket":"b-out"},"presets":["mp3"]}`); s.(TranscoderSpec).Output.Prefix != "transcoded/" {
		t.Error("default output folder")
	}
	cases := map[string]string{
		`{"input":{"bucket":"up"},"output":{"bucket":"up"},"presets":["mp3"]}`:                                   "spec.output.prefix:NOT_ALLOWED",
		`{"input":{"bucket":"upl","prefix":"v/"},"output":{"bucket":"upl","prefix":"v/out/"},"presets":["mp3"]}`: "spec.output.prefix:NOT_ALLOWED",
		`{"input":{"bucket":"upl","prefix":"../x"},"output":{"bucket":"oth"},"presets":["mp3"]}`:                 "spec.input.prefix:INVALID_FORMAT",
		`{"input":{"bucket":"upl"},"output":{"bucket":"oth"},"presets":[]}`:                                      "spec.presets:REQUIRED",
		`{"input":{"bucket":"upl"},"output":{"bucket":"oth"},"presets":["gif"]}`:                                 "spec.presets[0]:NOT_ALLOWED",
		`{"input":{"bucket":"upl"},"output":{"bucket":"oth"},"presets":["mp3","mp3"]}`:                           "spec.presets[1]:NOT_ALLOWED",
		`{"input":{},"output":{"bucket":"oth"},"presets":["mp3"]}`:                                               "spec.input.bucket:REQUIRED",
		`{"input":{"bucket":"upl"},"output":{"bucket":"oth"},"presets":["mp3"],"concurrency":5}`:                 "spec.concurrency:OUT_OF_RANGE",
		`{"input":{"bucket":"upl"},"output":{"bucket":"oth"},"presets":["mp3"],"timeoutSeconds":10}`:             "spec.timeoutSeconds:OUT_OF_RANGE",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindTranscoder, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	if e := ValidateTranscodeKey("other/a.mov", "videos/"); len(e) != 1 || e[0].Code != FieldNotAllowed {
		t.Errorf("key outside the folder: %v", e)
	}
	if e := ValidateTranscodeKey("videos/a.mov", "videos/"); e != nil {
		t.Errorf("key refused: %v", e)
	}
}

func TestCDNValidation(t *testing.T) {
	s, errs := normalize(t, KindCDN, `{"hostname":"CDN.Example.com.","origin":"app/web"}`)
	c := s.(CDNSpec)
	if len(errs) != 0 || c.Hostname != "cdn.example.com" || c.TLS != TLSAuto || !*c.RedirectHTTP || c.TTL() != 3600 || c.MaxSizeMiB != 1024 {
		t.Fatalf("defaults: %+v %v", c, errs)
	}
	if s, errs := normalize(t, KindCDN, `{"hostname":"assets.localhost","origin":"bucket/uploads","defaultTtlSeconds":0}`); len(errs) != 0 || s.(CDNSpec).TLS != TLSInternal || s.(CDNSpec).TTL() != 0 {
		t.Errorf("localhost bucket CDN: %+v %v", s, errs)
	}
	cases := map[string]string{
		`{"hostname":"cdn.example.com"}`:                                           "spec.origin:REQUIRED",
		`{"hostname":"cdn.example.com","origin":"vm/web"}`:                         "spec.origin:INVALID_FORMAT",
		`{"hostname":"cdn.example.com","origin":"bucket/uploads","originPort":80}`: "spec.originPort:NOT_ALLOWED",
		`{"hostname":"*.example.com","origin":"app/web","zone":"example"}`:         "spec.hostname:NOT_ALLOWED",
		`{"origin":"app/web"}`:                                                     "spec.hostname:REQUIRED",
		`{"hostname":"cdn.example.com","origin":"app/web","maxSizeMiB":10}`:        "spec.maxSizeMiB:OUT_OF_RANGE",
		`{"hostname":"cdn.example.com","origin":"app/web","defaultTtlSeconds":-1}`: "spec.defaultTtlSeconds:OUT_OF_RANGE",
		`{"hostname":"cdn.localhost","origin":"app/web","tls":"auto"}`:             "spec.tls:NOT_ALLOWED",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindCDN, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	if v := ImmutableViolations(KindCDN, CDNSpec{Hostname: "a.example.com"}, CDNSpec{Hostname: "b.example.com"}); len(v) != 1 {
		t.Error("hostname change allowed")
	}
}

func TestEmailValidation(t *testing.T) {
	s, errs := normalize(t, KindEmail, `{}`)
	if len(errs) != 0 || s.(EmailSpec).Mode != EmailCapture || s.(EmailSpec).KeepMessages != 500 {
		t.Errorf("defaults: %+v %v", s, errs)
	}
	s, errs = normalize(t, KindEmail, `{"mode":"relay","relay":{"host":"smtp.example.com","credentialsSecret":"smtp-login"},"domain":"Example.com"}`)
	if r := s.(EmailSpec).Relay; len(errs) != 0 || r.Port != 587 || r.TLS != RelaySTARTTLS || s.(EmailSpec).Domain != "example.com" {
		t.Errorf("relay defaults: %+v %v", s, errs)
	}
	cases := map[string]string{
		`{"mode":"relay"}`: "spec.relay:REQUIRED",
		`{"relay":{"host":"smtp.example.com","credentialsSecret":"x-yz"}}`:                            "spec.relay:NOT_ALLOWED",
		`{"mode":"relay","relay":{"host":"","credentialsSecret":"x-yz"}}`:                             "spec.relay.host:INVALID_FORMAT",
		`{"mode":"relay","relay":{"host":"smtp.example.com"}}`:                                        "spec.relay.credentialsSecret:REQUIRED",
		`{"mode":"relay","relay":{"host":"smtp.example.com","tls":"ssl","credentialsSecret":"x-yz"}}`: "spec.relay.tls:NOT_ALLOWED",
		`{"mode":"direct"}`:  "spec.mode:NOT_ALLOWED",
		`{"keepMessages":5}`: "spec.keepMessages:OUT_OF_RANGE",
		`{"domain":"nodot"}`: "spec.domain:INVALID_FORMAT",
	}
	for spec, want := range cases {
		if _, errs := normalize(t, KindEmail, spec); !slices.Contains(errs, want) {
			t.Errorf("%s: want %s, got %v", spec, want, errs)
		}
	}
	if _, errs := normalize(t, KindEmail, `{"mode":"relay","relay":{"host":"10.0.0.5","port":25,"tls":"none","credentialsSecret":"x-yz"}}`); len(errs) != 0 {
		t.Errorf("IP relay refused: %v", errs)
	}
}

func TestMessageAndEmailBodies(t *testing.T) {
	if e := ValidateSendMessage(SendMessage{Body: "hi", Attributes: map[string]string{"kind": "receipt"}, DeduplicationID: "order-1"}, 1024); e != nil {
		t.Errorf("valid message: %v", e)
	}
	if e := fieldsOf(ValidateSendMessage(SendMessage{Body: strings.Repeat("x", 1025)}, 1024)); !slices.Contains(e, "body:TOO_LONG") {
		t.Errorf("oversized body: %v", e)
	}
	if e := fieldsOf(ValidateSendMessage(SendMessage{Body: "x", Attributes: map[string]string{"1bad": "v", "ok": "a\nb"}}, 1024)); !slices.Contains(e, "attributes.1bad:INVALID_FORMAT") || !slices.Contains(e, "attributes.ok:INVALID_FORMAT") {
		t.Errorf("attributes: %v", e)
	}
	if e := fieldsOf(ValidateSendMessage(SendMessage{Body: "\xff"}, 1024)); !slices.Contains(e, "body:INVALID_FORMAT") {
		t.Errorf("binary body: %v", e)
	}
	if r, e := NormalizeReceive(ReceiveMessages{}); e != nil || r.Max != 1 {
		t.Errorf("receive defaults: %+v %v", r, e)
	}
	if _, e := NormalizeReceive(ReceiveMessages{Max: 11, WaitSeconds: 21}); len(e) != 2 {
		t.Errorf("receive limits: %v", e)
	}
	if ValidateDeleteMessages(DeleteMessages{}) == nil {
		t.Error("empty delete accepted")
	}
	ok := SendEmail{From: "shop@example.com", To: []string{"a@b.test"}, Subject: "Hi", Text: "Hello"}
	if e := ValidateSendEmail(ok, "example.com"); e != nil {
		t.Errorf("valid email: %v", e)
	}
	bad := ok
	bad.From, bad.Subject, bad.Text = "x@other.test", "a\r\nBcc: victim@x.test", ""
	if e := fieldsOf(ValidateSendEmail(bad, "example.com")); !slices.Equal(e, []string{"from:NOT_ALLOWED", "subject:INVALID_FORMAT", "text:REQUIRED"}) {
		t.Errorf("bad email: %v", e)
	}
	inj := ok
	inj.To = []string{"a@b.test>, c@d.test"}
	if e := fieldsOf(ValidateSendEmail(inj, "")); !slices.Contains(e, "to[0]:INVALID_FORMAT") {
		t.Errorf("recipient injection: %v", e)
	}
}

func TestWebhookURLs(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://hooks.example.com/a": true, "http://localhost:8080/h": true, "http://127.0.0.1/h": true,
		"http://example.com/h": false, "ftp://example.com": false, "https://": false, "https://a.example/#x": false,
	} {
		if ValidWebhookURL(raw) != want {
			t.Errorf("%s: want %v", raw, want)
		}
	}
}
