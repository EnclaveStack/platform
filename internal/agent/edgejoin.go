package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/version"
	"github.com/enclavestack/platform/pkg/api"
)

// Joining an edge machine from the main machine (L2-014 EDG-02, ADR-052). The edge machine can be reached;
// the main machine often can't. So the agent waits, and the main machine comes to it. Both prove they hold
// the join code with HMACs over each other's nonces; the agent's also covers the request it sends and the
// key of the temporary certificate the main machine saw, so nobody in between can swap either.

// PathEdgeJoin is the one route a waiting edge agent serves.
const PathEdgeJoin = "/agent/v1/join"

// EdgeJoinMaxFailures is how many wrong proofs a waiting agent takes before it stops listening.
const EdgeJoinMaxFailures = 5

// EdgeJoinKey is a join's proof key, from its code. Both sides compute it; the control plane keeps it
// sealed until the join ends.
func EdgeJoinKey(code string) []byte {
	m := hmac.New(sha256.New, []byte(strings.TrimSpace(code)))
	m.Write([]byte("enclave-edge-join"))
	return m.Sum(nil)
}

// EdgeOffer starts a join: the control plane's nonce.
type EdgeOffer struct {
	Step  string `json:"step"` // "offer"
	Nonce string `json:"nonce"`
}

// EdgeOfferReply is the agent's request and facts, proven with the key.
type EdgeOfferReply struct {
	CSR          string `json:"csr"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agentVersion"`
	AgentPort    int    `json:"agentPort"`
	TunnelPort   int    `json:"tunnelPort"`
	AgentNonce   string `json:"agentNonce"`
	MAC          string `json:"mac"`
}

// EdgeAccept finishes a join: the agent's identity, proven with the key.
type EdgeAccept struct {
	Step        string `json:"step"` // "accept"
	MachineID   string `json:"machineId"`
	Name        string `json:"name"`
	Certificate string `json:"certificate"`
	CA          string `json:"ca"`
	MAC         string `json:"mac"`
}

func mac(key []byte, parts ...string) string {
	m := hmac.New(sha256.New, key)
	for _, p := range parts {
		m.Write([]byte(p))
		m.Write([]byte{0})
	}
	return hex.EncodeToString(m.Sum(nil))
}

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// OfferMAC proves the agent's reply: both nonces, the request and the temporary certificate's key.
func OfferMAC(key []byte, nonce, agentNonce, csr string, spki []byte) string {
	return mac(key, "offer", nonce, agentNonce, digest([]byte(csr)), digest(spki))
}

// AcceptMAC proves the control plane's answer: the agent's nonce and the certificate it signed.
func AcceptMAC(key []byte, agentNonce, certificate string) string {
	return mac(key, "accept", agentNonce, digest([]byte(certificate)))
}

// NewNonce is 16 random bytes, hex.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// EdgeJoinOptions is what `enclave agent join --edge` is told.
type EdgeJoinOptions struct {
	Code       string
	Listen     string // default ":7071"
	TunnelPort int    // default 51820
	StateDir   string
	Log        *slog.Logger
	// Ready, when set, receives the address the agent waits on.
	Ready chan<- string
}

// WaitForMain waits for the main machine to join this computer as an edge machine, then saves the identity
// it gave. It returns once joined, or with an error after too many wrong proofs.
func WaitForMain(ctx context.Context, o EdgeJoinOptions) (Identity, error) {
	if o.StateDir == "" {
		o.StateDir = DefaultStateDir()
	}
	if _, err := Load(o.StateDir); err == nil {
		return Identity{}, fmt.Errorf("this computer already joined an EnclaveStack (%s). Run `enclave agent leave` first", o.StateDir)
	}
	if o.Listen == "" {
		o.Listen = ":" + strconv.Itoa(api.AgentPortDefault)
	}
	if o.TunnelPort == 0 {
		o.TunnelPort = api.TunnelPortDefault
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	_, portS, err := net.SplitHostPort(o.Listen)
	if err != nil {
		return Identity{}, fmt.Errorf("--listen %q: %w", o.Listen, err)
	}
	port, err := strconv.Atoi(portS)
	if err != nil {
		return Identity{}, fmt.Errorf("--listen %q: the port isn't a number", o.Listen)
	}
	keyPEM, csrPEM, err := NewKeyAndRequest()
	if err != nil {
		return Identity{}, err
	}
	temp, spki, err := temporaryCertificate()
	if err != nil {
		return Identity{}, err
	}
	ln, err := tls.Listen("tcp", o.Listen, &tls.Config{Certificates: []tls.Certificate{temp}, MinVersion: tls.VersionTLS13})
	if err != nil {
		return Identity{}, fmt.Errorf("listen on %s: %w", o.Listen, err)
	}
	w := &edgeWaiter{key: EdgeJoinKey(o.Code), csr: string(csrPEM), spki: spki, port: port, tunnelPort: o.TunnelPort, done: make(chan EdgeAccept, 1), failed: make(chan error, 1), log: o.Log}
	srv := &http.Server{Handler: w, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close() // the handler flushes its last reply before it says the wait is over
	if o.Ready != nil {
		o.Ready <- ln.Addr().String()
	}
	o.Log.Info("waiting for the main machine to join this edge machine", "listen", ln.Addr().String())
	select {
	case <-ctx.Done():
		return Identity{}, ctx.Err()
	case err := <-w.failed:
		return Identity{}, err
	case acc := <-w.done:
		id := Identity{MachineID: acc.MachineID, Name: acc.Name, Listen: o.Listen, TunnelPort: o.TunnelPort}
		if err := Save(o.StateDir, id, keyPEM, []byte(acc.Certificate), []byte(acc.CA)); err != nil {
			return Identity{}, err
		}
		id.dir = o.StateDir
		return id, nil
	}
}

type edgeWaiter struct {
	key        []byte
	csr        string
	spki       []byte
	port       int
	tunnelPort int
	log        *slog.Logger

	mu         sync.Mutex
	agentNonce string
	failures   int
	finished   bool
	done       chan EdgeAccept
	failed     chan error
}

func (w *edgeWaiter) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != PathEdgeJoin || r.Method != http.MethodPost {
		http.NotFound(rw, r)
		return
	}
	var msg struct {
		Step, Nonce, MachineID, Name, Certificate, CA, MAC string
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&msg); err != nil {
		http.Error(rw, "bad request", http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		http.Error(rw, "joined already", http.StatusGone)
		return
	}
	switch msg.Step {
	case "offer":
		nonce, err := NewNonce()
		if err != nil || len(msg.Nonce) < 32 {
			http.Error(rw, "bad nonce", http.StatusBadRequest)
			return
		}
		w.agentNonce = nonce
		reply := EdgeOfferReply{CSR: w.csr, OS: runtime.GOOS, Arch: runtime.GOARCH, AgentVersion: version.Version, AgentPort: w.port, TunnelPort: w.tunnelPort,
			AgentNonce: nonce, MAC: OfferMAC(w.key, msg.Nonce, nonce, w.csr, w.spki)}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(reply)
	case "accept":
		acc := EdgeAccept{Step: msg.Step, MachineID: msg.MachineID, Name: msg.Name, Certificate: msg.Certificate, CA: msg.CA, MAC: msg.MAC}
		if w.agentNonce == "" || !hmac.Equal([]byte(acc.MAC), []byte(AcceptMAC(w.key, w.agentNonce, acc.Certificate))) {
			w.failures++
			w.log.Warn("a join attempt with the wrong code", "from", r.RemoteAddr, "failures", w.failures)
			http.Error(rw, "wrong proof", http.StatusUnauthorized)
			if w.failures >= EdgeJoinMaxFailures {
				w.finished = true
				flush(rw) // the listener closes as soon as the wait ends
				w.failed <- errors.New("five join attempts with the wrong code: stopped listening. Check the code, then run the command again")
			}
			return
		}
		w.finished = true
		rw.WriteHeader(http.StatusNoContent)
		flush(rw)
		w.done <- acc
	default:
		http.Error(rw, "unknown step", http.StatusBadRequest)
	}
}

// temporaryCertificate is a self-signed certificate for the join only; the main machine checks its key
// through the offer's proof, not through any CA.
func temporaryCertificate() (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "enclave-edge-join"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(2 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, spki, nil
}

// flush sends what a handler wrote now, before the server is closed under it.
func flush(rw http.ResponseWriter) {
	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}
}
