package service

import (
	"net"
	"strings"
	"testing"
)

// Links in alerts and join commands go where people and other computers can open them: the console URL
// given, else a listen address others can reach; a loopback address makes none (NCH-02, MCH-02).
func TestServerURL(t *testing.T) {
	for _, c := range []struct{ console, listen, want string }{
		{"https://enclave.example.com", "http://127.0.0.1:7070", "https://enclave.example.com"},
		{"", "http://127.0.0.1:7070", ""},
		{"", "http://localhost:7070", ""},
		{"", "http://[::1]:7070", ""},
		{"", "http://192.168.1.20:7070", "http://192.168.1.20:7070"},
	} {
		if got := ServerURL(c.console, c.listen); got != c.want {
			t.Errorf("ServerURL(%q, %q) = %q, want %q", c.console, c.listen, got, c.want)
		}
	}
	// Listening on every interface: this computer's first private address, or nothing without one.
	got := ServerURL("", "http://0.0.0.0:7070")
	if got != "" {
		host, port, err := net.SplitHostPort(strings.TrimPrefix(got, "http://"))
		if ip := net.ParseIP(host); err != nil || port != "7070" || ip == nil || !ip.IsPrivate() {
			t.Errorf("ServerURL on 0.0.0.0 = %q", got)
		}
	}
}

func TestJoinCodesAreLongAndForgiving(t *testing.T) {
	code, hash := newJoinCode()
	if len(strings.ReplaceAll(code, "-", "")) != 32 {
		t.Errorf("code %q", code)
	}
	if hashCode(strings.ToLower(code)) != hash || hashCode(strings.ReplaceAll(code, "-", "")) != hash || hashCode(" "+code+" ") != hash {
		t.Error("a code typed in lowercase, without dashes or with spaces doesn't match")
	}
	if other, _ := newJoinCode(); other == code {
		t.Error("two codes are the same")
	}
}
