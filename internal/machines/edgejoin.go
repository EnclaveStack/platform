package machines

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Joining edge machines from the main machine (L2-014 EDG-02, ADR-052): each round, the control plane
// calls every edge machine still waiting at its address and joins it if the agent there proves it holds
// the code.

// EdgeJoinTimeout bounds one try (L3-007 §14).
const EdgeJoinTimeout = 10 * time.Second

// JoinKeyAAD binds a sealed edge join key to its machine.
func JoinKeyAAD(machineID string) []byte { return []byte("edge-join:" + machineID) }

// joinEdges tries every waiting edge machine whose code hasn't expired.
func (m *Manager) joinEdges(ctx context.Context, all []store.Machine) {
	for _, mc := range all {
		if !mc.IsEdge() || mc.State != api.MachineWaiting || len(mc.JoinKey) == 0 || mc.JoinExpiresAt == nil || m.Now().After(*mc.JoinExpiresAt) {
			continue
		}
		if err := m.joinEdge(ctx, mc); err != nil {
			m.Log.Debug("edge machine not joined yet", "machine", mc.Name, "error", err)
		}
	}
}

// joinEdge runs one join with an edge machine's waiting agent.
func (m *Manager) joinEdge(ctx context.Context, mc store.Machine) error {
	key, err := m.Box.Open(mc.JoinKey, JoinKeyAAD(mc.ID))
	if err != nil {
		return fmt.Errorf("the join key can't be opened: %w", err)
	}
	var seen []byte                                                                                            // the temporary certificate's key, as this side saw it
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, //nolint:gosec // checked by the offer's proof
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("no certificate")
			}
			c, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			seen = c.RawSubjectPublicKeyInfo
			return nil
		}}}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: EdgeJoinTimeout}
	port := mc.AgentPort
	if port == 0 {
		port = api.AgentPortDefault
	}
	url := "https://" + net.JoinHostPort(mc.Address, strconv.Itoa(port)) + agent.PathEdgeJoin
	nonce, err := agent.NewNonce()
	if err != nil {
		return err
	}
	var offer agent.EdgeOfferReply
	if err := post(ctx, hc, url, agent.EdgeOffer{Step: "offer", Nonce: nonce}, &offer); err != nil {
		return err
	}
	if !hmac.Equal([]byte(offer.MAC), []byte(agent.OfferMAC(key, nonce, offer.AgentNonce, offer.CSR, seen))) {
		m.addEvent(ctx, mc.ID, "join_refused", "The computer at "+mc.Address+" didn't prove it holds the join code.")
		return errors.New("the agent's proof doesn't match the code")
	}
	ca, err := m.CA(ctx)
	if err != nil {
		return err
	}
	now := m.Now()
	cert, serial, notAfter, err := ca.SignAgent([]byte(offer.CSR), mc.ID, now)
	if err != nil {
		return err
	}
	acc := agent.EdgeAccept{Step: "accept", MachineID: mc.ID, Name: mc.Name, Certificate: string(cert), CA: string(ca.PEM),
		MAC: agent.AcceptMAC(key, offer.AgentNonce, string(cert))}
	if err := post(ctx, hc, url, acc, nil); err != nil {
		return err
	}
	mc.State, mc.StateSince = api.MachineOffline, now // online at the next check, over mutual TLS
	mc.JoinCodeHash, mc.JoinExpiresAt, mc.JoinKey = "", nil, nil
	mc.AgentPort, mc.TunnelPort = offer.AgentPort, offer.TunnelPort
	mc.CertSerial, mc.CertNotAfter = serial, &notAfter
	mc.Info = store.MachineInfo{OS: offer.OS, Arch: offer.Arch, AgentVersion: offer.AgentVersion}
	err = m.Store.WithTx(ctx, func(q *store.Queries) error {
		if err := q.SaveMachine(ctx, mc); err != nil {
			return err
		}
		return q.AddMachineEvent(ctx, mc.ID, "joined", fmt.Sprintf("Joined as an edge machine at %s (agent port %d).", mc.Address, mc.AgentPort), now)
	})
	if err != nil {
		return err
	}
	m.Log.Info("edge machine joined", "machine", mc.Name, "address", mc.Address)
	return nil
}

func (m *Manager) addEvent(ctx context.Context, machineID, typ, msg string) {
	if err := m.Store.AddMachineEvent(ctx, machineID, typ, msg, m.Now()); err != nil {
		m.Log.Warn("record machine event failed", "error", err)
	}
}

func post(ctx context.Context, hc *http.Client, url string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("the agent answered %s: %s", resp.Status, bytes.TrimSpace(raw))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
