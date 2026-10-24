package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The edge join's proofs (ADR-052): the right code joins, a wrong one never does, and five wrong proofs
// stop the agent listening.

func waitingAgent(t *testing.T, code string) (string, chan error, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	dir := filepath.Join(t.TempDir(), "state")
	ready, done := make(chan string, 1), make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_, err := WaitForMain(ctx, EdgeJoinOptions{Code: code, Listen: "127.0.0.1:" + strconv.Itoa(port), StateDir: dir, Ready: ready})
		done <- err
	}()
	select {
	case addr := <-ready:
		return "https://" + addr + PathEdgeJoin, done, dir
	case <-time.After(5 * time.Second):
		t.Fatal("the agent didn't start waiting")
	}
	return "", nil, ""
}

// joinClient posts as the control plane would, recording the certificate's key it saw.
// Handshakes may run side by side (a retried connection), so the record is guarded.
func joinClient(seen *[]byte) *http.Client {
	var mu sync.Mutex
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, //nolint:gosec // as the control plane: the proof checks it
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			c, err := x509.ParseCertificate(raw[0])
			if err == nil {
				mu.Lock()
				*seen = c.RawSubjectPublicKeyInfo
				mu.Unlock()
			}
			return err
		}}}}
}

func postJSON(t *testing.T, c *http.Client, url string, body, out any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := c.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestEdgeJoinProofs(t *testing.T) {
	url, done, dir := waitingAgent(t, "RIGHT-CODE")
	var seen []byte
	c := joinClient(&seen)
	nonce, _ := NewNonce()
	var offer EdgeOfferReply
	if code := postJSON(t, c, url, EdgeOffer{Step: "offer", Nonce: nonce}, &offer); code != http.StatusOK {
		t.Fatalf("offer: %d", code)
	}
	right, wrong := EdgeJoinKey("RIGHT-CODE"), EdgeJoinKey("WRONG-CODE")
	if offer.MAC != OfferMAC(right, nonce, offer.AgentNonce, offer.CSR, seen) {
		t.Fatal("the agent's proof must check with the right code and the certificate seen")
	}
	if offer.MAC == OfferMAC(wrong, nonce, offer.AgentNonce, offer.CSR, seen) {
		t.Fatal("the agent's proof must not check with another code")
	}
	if offer.MAC == OfferMAC(right, nonce, offer.AgentNonce, offer.CSR, []byte("another key")) {
		t.Fatal("a swapped certificate must not check")
	}
	if !strings.Contains(offer.CSR, "CERTIFICATE REQUEST") || offer.AgentPort == 0 {
		t.Fatalf("offer %+v", offer)
	}
	// A control plane with the wrong code can't join it.
	if code := postJSON(t, c, url, EdgeAccept{Step: "accept", MachineID: "mch_x", Name: "vps1", Certificate: "c", CA: "ca", MAC: AcceptMAC(wrong, offer.AgentNonce, "c")}, nil); code != http.StatusUnauthorized {
		t.Fatalf("accept with the wrong code: %d", code)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("nothing must be saved after a wrong proof")
	}
	// The right one joins.
	if code := postJSON(t, c, url, EdgeAccept{Step: "accept", MachineID: "mch_x", Name: "vps1", Certificate: "cert-pem", CA: "ca-pem", MAC: AcceptMAC(right, offer.AgentNonce, "cert-pem")}, nil); code != http.StatusNoContent {
		t.Fatalf("accept: %d", code)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if id, err := Load(dir); err != nil || id.MachineID != "mch_x" || id.Name != "vps1" {
		t.Fatalf("identity %+v %v", id, err)
	}
}

func TestEdgeJoinStopsAfterFiveWrongProofs(t *testing.T) {
	url, done, _ := waitingAgent(t, "RIGHT-CODE")
	var seen []byte
	c := joinClient(&seen)
	nonce, _ := NewNonce()
	var offer EdgeOfferReply
	postJSON(t, c, url, EdgeOffer{Step: "offer", Nonce: nonce}, &offer)
	for i := 0; i < EdgeJoinMaxFailures; i++ {
		postJSON(t, c, url, EdgeAccept{Step: "accept", Certificate: "c", MAC: AcceptMAC(EdgeJoinKey("guess"), offer.AgentNonce, "c")}, nil)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stopped listening") {
			t.Fatalf("after five wrong proofs: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the agent kept listening after five wrong proofs")
	}
}
