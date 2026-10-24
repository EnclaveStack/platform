package httpapi

import (
	"io"
	"strings"
	"testing"
)

// An upload without a length is cut off just past the limit and reported as too large; one of exactly
// the limit passes whole (OBJ-02).
func TestUploadLimitWithoutALength(t *testing.T) {
	exact := &tooLarge{r: strings.NewReader("12345"), left: 5}
	if b, err := io.ReadAll(exact); err != nil || string(b) != "12345" || exact.hit {
		t.Errorf("exactly the limit: %q %v %v", b, err, exact.hit)
	}
	over := &tooLarge{r: strings.NewReader("123456"), left: 5}
	if _, err := io.ReadAll(over); err == nil || !over.hit {
		t.Errorf("past the limit: %v %v", err, over.hit)
	}
}
