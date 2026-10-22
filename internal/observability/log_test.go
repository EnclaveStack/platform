package observability

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerRedactsSensitiveAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := NewLogger(&buf, "debug", false)
	log.Info("event", "password", "hunter2", "token", "hc_pat_abc", "secret_value", "s3", "Authorization", "Bearer x",
		"cookie", "es_session=y", "setup_code", "AAAA-BBBB-CCCC", "resource_id", "app_123")
	log.With("value", "nested-secret").Warn("child")
	out := buf.String()
	for _, leak := range []string{"hunter2", "hc_pat_abc", "s3\"", "Bearer x", "es_session=y", "AAAA-BBBB-CCCC", "nested-secret"} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "app_123") || !strings.Contains(out, `"password":"[redacted]"`) {
		t.Errorf("non-sensitive attributes lost or redaction marker missing:\n%s", out)
	}
}

func TestLevelsAndFormats(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, "warn", false).Info("hidden")
	if buf.Len() != 0 {
		t.Error("info logged at warn level")
	}
	NewLogger(&buf, "info", true).Info("shown", "k", "v")
	if !strings.Contains(buf.String(), "msg=shown") {
		t.Errorf("text format: %q", buf.String())
	}
}
