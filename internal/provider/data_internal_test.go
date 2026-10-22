package provider

import "testing"

// A transcoder's folder must be written as the server stores it, so a plan after apply shows no change.
func TestPrefixPatternAcceptsOnlyNormalisedFolders(t *testing.T) {
	for p, want := range map[string]bool{"": true, "videos/": true, "a/b/": true, "/videos": false, "videos": false, "/": false} {
		if prefixPattern.MatchString(p) != want {
			t.Errorf("%q: want %v", p, want)
		}
	}
}
