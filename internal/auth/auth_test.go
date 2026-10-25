package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHashRoundTripAndMismatch(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Errorf("unexpected PHC string %q", h)
	}
	if err := VerifyPassword("correct horse battery staple", h); err != nil {
		t.Errorf("right password rejected: %v", err)
	}
	if err := VerifyPassword("wrong", h); err != ErrMismatch {
		t.Errorf("wrong password: %v", err)
	}
	h2, _ := HashPassword("correct horse battery staple")
	if h == h2 {
		t.Error("salts are not random")
	}
	if err := VerifyPassword("x", "$bcrypt$nope"); err == nil {
		t.Error("foreign hash accepted")
	}
}

func TestTokensAndSessionsAreRandomAndHashed(t *testing.T) {
	a, pa := NewToken()
	b, _ := NewToken()
	if a == b || !strings.HasPrefix(a, "es_pat_") || len(a) < 50 || !strings.HasPrefix(a, pa) {
		t.Errorf("token %q prefix %q", a, pa)
	}
	h1, h2 := Hash(a), Hash(a)
	if h1 == a || len(h1) != 64 || h1 != h2 {
		t.Error("hash is not a stable SHA-256 hex")
	}
	if s1, s2 := NewSessionID(), NewSessionID(); s1 == s2 {
		t.Error("session IDs repeat")
	}
	if !EqualHash(Hash("x"), Hash("x")) || EqualHash(Hash("x"), Hash("y")) {
		t.Error("EqualHash wrong")
	}
}

func TestSetupCodeFormatAndNormalisation(t *testing.T) {
	code := NewSetupCode()
	if len(code) != 14 || code[4] != '-' || code[9] != '-' || strings.ContainsAny(code, "01IO") {
		t.Errorf("setup code %q", code)
	}
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	if NormalizeSetupCode(typed) != code {
		t.Errorf("normalise(%q) = %q, want %q", typed, NormalizeSetupCode(typed), code)
	}
}

func TestThrottleBlocksAfterMaxFailuresAndRecovers(t *testing.T) {
	now := time.Unix(1000, 0)
	th := NewThrottle(3, time.Minute, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if blocked, _ := th.Blocked("k"); blocked {
			t.Fatalf("blocked after %d failures", i)
		}
		th.Fail("k")
		now = now.Add(time.Second)
	}
	blocked, wait := th.Blocked("k")
	if !blocked || wait <= 0 || wait > time.Minute {
		t.Fatalf("want blocked with wait in (0,1m], got %v %v", blocked, wait)
	}
	if b, _ := th.Blocked("other"); b {
		t.Error("throttle leaked across keys")
	}
	now = now.Add(time.Minute)
	if b, _ := th.Blocked("k"); b {
		t.Error("still blocked after the window")
	}
	th.Fail("k")
	th.Succeed("k")
	if b, _ := th.Blocked("k"); b {
		t.Error("success did not clear failures")
	}
}

func TestVerifyDummyTakesTheRealPath(t *testing.T) {
	// Unknown-email sign-ins must cost a real argon2id verification (no user enumeration by timing).
	start := time.Now()
	VerifyDummy("anything")
	if time.Since(start) < 5*time.Millisecond {
		t.Errorf("dummy verification took %s: too fast to be a real argon2id check", time.Since(start))
	}
}
