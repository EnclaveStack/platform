package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
)

// Identity is what an agent keeps in its state directory (L3-005 §10): its key and certificate, the CA,
// and where it belongs.
type Identity struct {
	MachineID  string `json:"machineId"`
	Name       string `json:"name"`
	Server     string `json:"server"`
	Advertise  string `json:"advertise"`
	Listen     string `json:"listen"`
	TunnelPort int    `json:"tunnelPort"`
	// Standby keeps copies of the control plane and can be promoted (ADR-057).
	Standby bool `json:"standby,omitempty"`

	dir string
}

const (
	fileKey      = "key.pem"
	fileCert     = "cert.pem"
	fileCA       = "ca.pem"
	fileSettings = "agent.json"
)

// DefaultStateDir is ~/.enclave-agent.
func DefaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".enclave-agent"
	}
	return filepath.Join(home, ".enclave-agent")
}

// ErrNotJoined means the state directory holds no identity: run `enclave agent join` first.
var ErrNotJoined = errors.New("this computer hasn't joined an EnclaveStack: run `enclave agent join` first")

// Save writes a new identity; the key file is readable by the owner only.
func Save(dir string, id Identity, keyPEM, certPEM, caPEM []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	settings, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	for name, body := range map[string][]byte{fileKey: keyPEM, fileCert: certPEM, fileCA: caPEM, fileSettings: settings} {
		if err := writeFileAtomic(filepath.Join(dir, name), body); err != nil {
			return err
		}
	}
	return nil
}

// Load reads an identity, or ErrNotJoined.
func Load(dir string) (Identity, error) {
	b, err := os.ReadFile(filepath.Join(dir, fileSettings))
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, ErrNotJoined
	}
	if err != nil {
		return Identity{}, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return Identity{}, fmt.Errorf("%s: %w", fileSettings, err)
	}
	id.dir = dir
	return id, nil
}

// Remove deletes the identity (`enclave agent leave`).
func Remove(dir string) error { return os.RemoveAll(dir) }

func (id Identity) read(name string) ([]byte, error) { return os.ReadFile(filepath.Join(id.dir, name)) }

// Dir is where the identity lives.
func (id Identity) Dir() string { return id.dir }

// certs keeps the agent's certificate, swapped in place on renewal.
type certs struct {
	mu      sync.Mutex
	current *tls.Certificate
	pending []byte // the key of a renewal in progress
	id      Identity
}

func (c *certs) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current, nil
}

// ServerTLS loads the identity's TLS configuration, with a certificate the renewal endpoints can replace.
func (id Identity) serverTLS() (*tls.Config, *certs, error) {
	keyPEM, err := id.read(fileKey)
	if err != nil {
		return nil, nil, err
	}
	certPEM, err := id.read(fileCert)
	if err != nil {
		return nil, nil, err
	}
	caPEM, err := id.read(fileCA)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := ServerConfig(certPEM, keyPEM, caPEM)
	if err != nil {
		return nil, nil, err
	}
	c := &certs{current: &cfg.Certificates[0], id: id}
	cfg.Certificates = nil
	cfg.GetCertificate = c.get
	return cfg, c, nil
}

// renew makes a new key and certificate request; the key stays pending until its certificate arrives.
func (c *certs) renew(w http.ResponseWriter, _ *http.Request) {
	keyPEM, csrPEM, err := NewKeyAndRequest()
	if err != nil {
		writeError(w, err)
		return
	}
	c.mu.Lock()
	c.pending = keyPEM
	c.mu.Unlock()
	writeResult(w, map[string]string{"csr": string(csrPEM)})
}

// install takes the renewed certificate, which must match the pending key, and keeps both on disk.
func (c *certs) install(w http.ResponseWriter, r *http.Request) {
	var body struct{ Certificate string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		http.Error(w, "no renewal in progress", http.StatusConflict)
		return
	}
	pair, err := tls.X509KeyPair([]byte(body.Certificate), c.pending)
	if err != nil {
		http.Error(w, "the certificate doesn't match the new key", http.StatusUnprocessableEntity)
		return
	}
	if err := writeFileAtomic(filepath.Join(c.id.dir, fileKey), c.pending); err != nil {
		writeError(w, err)
		return
	}
	if err := writeFileAtomic(filepath.Join(c.id.dir, fileCert), []byte(body.Certificate)); err != nil {
		writeError(w, err)
		return
	}
	c.current, c.pending = &pair, nil
	w.WriteHeader(http.StatusNoContent)
}

// Renew asks the agent for a new certificate request, signs it with sign, and installs the result
// (ADR-043). It returns the new certificate.
func (m *Remote) Renew(ctx context.Context, sign func(csrPEM []byte) ([]byte, error)) ([]byte, error) {
	res, err := call[map[string]string](ctx, m, pathRenew, none{})
	if err != nil {
		return nil, err
	}
	cert, err := sign([]byte(res["csr"]))
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(map[string]string{"Certificate": string(cert)})
	req, err := m.request(ctx, http.MethodPost, pathCert, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	resp, err := m.do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("the agent refused its new certificate: %s", b)
	}
	return cert, nil
}

func writeFileAtomic(path string, body []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
