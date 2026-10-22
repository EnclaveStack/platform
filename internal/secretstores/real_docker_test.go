//go:build docker

package secretstores

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// The Vault client against the real servers (L2-009 EXT-01, ADR-041): HashiCorp Vault and OpenBao, each
// run in dev mode in Docker. A secret written through the server's own API is read back, a missing
// key is ErrNotFound, and a wrong token is refused.
func TestVaultAndOpenBaoForReal(t *testing.T) {
	if exec.Command("docker", "info").Run() != nil {
		t.Skip("Docker is not running")
	}
	for _, server := range []struct{ name, image, cmd string }{
		{"vault", "hashicorp/vault:1.20.4", "vault"},
		{"openbao", "openbao/openbao:2.4.1", "bao"},
	} {
		t.Run(server.name, func(t *testing.T) {
			ctx := context.Background()
			port := freePort(t)
			container := fmt.Sprintf("es-it-%s-%d", server.name, port)
			out, err := exec.Command("docker", "run", "-d", "--rm", "--name", container, "-p", fmt.Sprintf("127.0.0.1:%d:8200", port),
				"--cap-add", "IPC_LOCK", "-e", "SKIP_SETCAP=1", server.image, "server", "-dev", "-dev-root-token-id=root", "-dev-listen-address=0.0.0.0:8200").CombinedOutput()
			if err != nil {
				t.Fatalf("start %s: %v %s", server.image, err, out)
			}
			t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
			addr := fmt.Sprintf("http://127.0.0.1:%d", port)
			waitHealthy(t, addr)

			// Write through the server's own API: KV v2 at secret/, as a person would.
			body, _ := json.Marshal(map[string]any{"data": map[string]string{"key": "sk_live_real", "other": "x"}})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, addr+"/v1/secret/data/shop/stripe", bytes.NewReader(body))
			req.Header.Set("X-Vault-Token", "root")
			res, err := http.DefaultClient.Do(req)
			if err != nil || res.StatusCode != http.StatusOK {
				t.Fatalf("write the secret: %v %v", res, err)
			}
			res.Body.Close()

			open := func(token string) Store {
				raw, _ := json.Marshal(api.SecretStoreSpec{Provider: api.ProviderVault, Vault: &api.VaultStore{Address: addr, TokenSecret: "vault-token"}})
				st, _, err := ForSpec(raw, func(string) (string, error) { return token, nil }, http.DefaultClient, time.Now)
				if err != nil {
					t.Fatal(err)
				}
				return st
			}
			st := open("root")
			if err := st.Test(ctx); err != nil {
				t.Fatalf("test: %v", err)
			}
			if v, err := st.Fetch(ctx, "shop/stripe", "key"); err != nil || v != "sk_live_real" {
				t.Errorf("field: %q %v", v, err)
			}
			// Several fields and none named: refused with the field names, never a guess.
			if _, err := st.Fetch(ctx, "shop/stripe", ""); err == nil || !strings.Contains(err.Error(), "several fields (key, other)") {
				t.Errorf("no field of several: %v", err)
			}
			if _, err := st.Fetch(ctx, "shop/nope", ""); err == nil || !strings.Contains(err.Error(), ErrNotFound.Error()) {
				t.Errorf("missing key: %v", err)
			}
			bad := open("wrong")
			if err := bad.Test(ctx); err == nil || !strings.Contains(err.Error(), "403") || strings.Contains(err.Error(), "wrong") {
				t.Errorf("a wrong token: %v", err)
			}
		})
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitHealthy(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if res, err := http.Get(addr + "/v1/sys/health"); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("%s never became healthy", addr)
}
