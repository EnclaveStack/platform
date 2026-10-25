package mail

import (
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

func TestComposeKeepsBccOutOfHeadersAndBuildsAlternatives(t *testing.T) {
	raw, id := Compose(api.SendEmail{From: "shop@example.com", To: []string{"a@b.test"}, Bcc: []string{"hidden@b.test"}, Subject: "Grüße",
		Text: "Hello\nthere", HTML: "<p>Hello</p>"}, time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC))
	s := string(raw)
	if strings.Contains(s, "hidden@b.test") {
		t.Error("Bcc leaked into the headers")
	}
	for _, want := range []string{"From: shop@example.com\r\n", "To: a@b.test\r\n", "Subject: =?utf-8?q?Gr=C3=BC=C3=9Fe?=\r\n", "multipart/alternative; boundary=",
		"Content-Type: text/plain; charset=utf-8", "Content-Type: text/html; charset=utf-8", "Hello\r\nthere", "Message-ID: " + id} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if !strings.HasSuffix(id, "@example.com>") {
		t.Errorf("message ID %s", id)
	}
	plain, _ := Compose(api.SendEmail{From: "a@b.test", To: []string{"c@d.test"}, Subject: "x", Text: "only text"}, time.Now())
	if strings.Contains(string(plain), "multipart") || !strings.Contains(string(plain), "text/plain") {
		t.Errorf("plain message:\n%s", plain)
	}
}
