// Package auth implements EnclaveStack's credentials (ADR-007, L3-006): argon2id password hashes,
// random session IDs and API tokens stored only as SHA-256 hashes, one-time setup codes, and the
// sign-in throttle.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"

	"github.com/enclavestack/platform/pkg/api"
)

// Argon2id parameters (L3-006 §3).
const (
	argonMemoryKiB = 64 * 1024
	argonTime      = 3
	argonThreads   = 2
	argonKeyLen    = 32
	saltLen        = 16
)

// ErrMismatch is returned when a password does not match its hash.
var ErrMismatch = errors.New("password does not match")

// HashPassword returns an argon2id PHC string.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword checks password against a PHC string in constant time.
func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return fmt.Errorf("unsupported password hash")
	}
	var version int
	var mem uint32
	var iters uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return fmt.Errorf("unsupported argon2 version")
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iters, &threads); err != nil {
		return fmt.Errorf("bad argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("bad salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("bad hash")
	}
	got := argon2.IDKey([]byte(password), salt, iters, mem, threads, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// dummyHash is verified against when an email is unknown, so both paths cost the same (no user
// enumeration). It is computed on first use: argon2id needs 64 MiB, and computing it at start-up made
// every CLI command (even `enclave version`) pay for it.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("enclave-timing-equaliser")
	return h
})

// VerifyDummy spends the same time as a real verification.
func VerifyDummy(password string) { _ = VerifyPassword(password, dummyHash()) }

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// RandomSecret returns n random bytes, base32-encoded lowercase.
func RandomSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return strings.ToLower(b32.EncodeToString(b))
}

// Hash is SHA-256 hex, used for session IDs, tokens and setup codes (high-entropy inputs).
func Hash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// NewSessionID returns a cookie value.
func NewSessionID() string { return RandomSecret(32) }

// NewToken returns a token secret "hc_pat_…" and its display prefix.
func NewToken() (secret, prefix string) {
	secret = api.TokenPrefix + RandomSecret(32)
	return secret, secret[:len(api.TokenPrefix)+6]
}

// NewSetupCode returns a human-typeable one-time code like "K7QD-3MXP-9TZA".
func NewSetupCode() string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	var sb strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			sb.WriteByte('-')
		}
		sb.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return sb.String()
}

// NormalizeSetupCode upper-cases and strips spaces so "k7qd 3mxp 9tza" matches.
func NormalizeSetupCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, " ", "")
	code = strings.ReplaceAll(code, "-", "")
	if len(code) == 12 {
		return code[0:4] + "-" + code[4:8] + "-" + code[8:12]
	}
	return code
}

// EqualHash compares two hex hashes in constant time.
func EqualHash(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// ReleaseMemory hands the 64 MiB argon2id working set back to the OS after a password operation.
// Go would otherwise keep it resident for minutes; the control plane shares the machine with user
// workloads (L5-003 idle footprint). It runs in the background, off the request path.
func ReleaseMemory() { go debug.FreeOSMemory() }
