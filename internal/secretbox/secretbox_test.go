package secretbox

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSealOpenBindsToAdditionalData(t *testing.T) {
	key := bytes.Repeat([]byte{7}, KeySize)
	b, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := b.Seal([]byte("s3cret"), []byte("sec_a"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ct, []byte("s3cret")) {
		t.Fatal("plaintext visible in ciphertext")
	}
	pt, err := b.Open(ct, []byte("sec_a"))
	if err != nil || string(pt) != "s3cret" {
		t.Fatalf("open: %q %v", pt, err)
	}
	if _, err := b.Open(ct, []byte("sec_b")); err != ErrOpen {
		t.Error("ciphertext moved to another resource still opens")
	}
	ct[len(ct)-1] ^= 1
	if _, err := b.Open(ct, []byte("sec_a")); err != ErrOpen {
		t.Error("tampered ciphertext opens")
	}
	other, _ := New(bytes.Repeat([]byte{8}, KeySize))
	ct2, _ := b.Seal([]byte("x"), nil)
	if _, err := other.Open(ct2, nil); err != ErrOpen {
		t.Error("wrong key opens")
	}
	ct3, _ := b.Seal([]byte("x"), nil)
	if bytes.Equal(ct2, ct3) {
		t.Error("nonce reused")
	}
	if _, err := New([]byte("short")); err == nil {
		t.Error("short key accepted")
	}
}

func TestLoadOrCreateKeyIsStableAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	k1, err := LoadOrCreateKey(path)
	if err != nil || len(k1) != KeySize {
		t.Fatalf("create: %v", err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Errorf("key file mode %v, want 0600", st.Mode().Perm())
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil || !bytes.Equal(k1, k2) {
		t.Fatalf("reload changed the key: %v", err)
	}
	_ = os.WriteFile(path, []byte("bad"), 0o600)
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Error("corrupt key accepted")
	}
}
