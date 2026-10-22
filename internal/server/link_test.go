package server

import "testing"

// Links in alerts go where people can open them: the console URL given, else a listen address others
// can reach; a loopback address makes no link (NCH-02).
func TestLinkBase(t *testing.T) {
	for _, c := range []struct{ console, listen, want string }{
		{"https://enclave.example.com", "http://127.0.0.1:7070", "https://enclave.example.com"},
		{"", "http://127.0.0.1:7070", ""},
		{"", "http://localhost:7070", ""},
		{"", "http://0.0.0.0:7070", ""},
		{"", "http://[::1]:7070", ""},
		{"", "http://192.168.1.20:7070", "http://192.168.1.20:7070"},
	} {
		if got := linkBase(c.console, c.listen); got != c.want {
			t.Errorf("linkBase(%q, %q) = %q, want %q", c.console, c.listen, got, c.want)
		}
	}
}
