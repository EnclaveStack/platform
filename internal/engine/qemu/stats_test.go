package qemu

import "testing"

func TestParsePS(t *testing.T) {
	for _, c := range []struct {
		in  string
		cpu float64
		rss int64
	}{{"0:01.50 2048\n", 1.5, 2048}, {"  01:02:03 100", 3723, 100}, {"1-00:00:01 5", 86401, 5}} {
		cpu, rss, err := parsePS(c.in)
		if err != nil || cpu != c.cpu || rss != c.rss {
			t.Errorf("parsePS(%q) = %v, %v, %v", c.in, cpu, rss, err)
		}
	}
	if _, _, err := parsePS("garbage"); err == nil {
		t.Error("garbage parsed")
	}
}
