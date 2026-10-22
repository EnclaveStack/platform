// Package agent runs another machine's engine for the control plane (ADR-042, ADR-043): a server on that
// machine that serves its Docker driver and hypervisor over mutual TLS, and Remote, the control plane's
// client, which implements engine.Driver and engine.Hypervisor. Both ends trust only the EnclaveStack
// certificate authority made by the control plane.
package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"time"
)

// Identity facts (ADR-043).
const (
	ControlPlaneCN        = "enclave-control-plane"
	caValidity            = 10 * 365 * 24 * time.Hour
	agentValidity         = 365 * 24 * time.Hour
	controlPlaneValidity  = 30 * 24 * time.Hour
	RenewBefore           = 60 * 24 * time.Hour
	machineURIPrefix      = "enclave://machine/"
	clockSkew             = 5 * time.Minute
	pemCertificate        = "CERTIFICATE"
	pemPrivateKey         = "EC PRIVATE KEY"
	pemCertificateRequest = "CERTIFICATE REQUEST"
)

// MachineURI is the URI SAN an agent's certificate carries.
func MachineURI(machineID string) string { return machineURIPrefix + machineID }

// CA is the certificate authority, parsed.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	PEM  []byte
}

// NewCA makes a CA valid 10 years; it returns the certificate and the key, both PEM.
func NewCA(now time.Time) (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "EnclaveStack machines CA"},
		NotBefore: now.Add(-clockSkew), NotAfter: now.Add(caValidity),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = encodeKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}), keyPEM, err
}

// ParseCA reads a CA certificate and key.
func ParseCA(certPEM, keyPEM []byte) (*CA, error) {
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, err
	}
	key, err := ParseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: key, PEM: certPEM}, nil
}

// SignAgent signs a joining agent's certificate request for machineID, valid one year (ADR-043). The
// request's own subject and names are ignored: only the key is taken.
func (ca *CA) SignAgent(csrPEM []byte, machineID string, now time.Time) (certPEM []byte, serialHex string, notAfter time.Time, err error) {
	block, _ := pem.Decode(csrPEM)
	if block == nil || block.Type != pemCertificateRequest {
		return nil, "", time.Time{}, errors.New("the certificate request isn't PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, "", time.Time{}, fmt.Errorf("read the certificate request: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, "", time.Time{}, fmt.Errorf("the certificate request's signature is wrong: %w", err)
	}
	if _, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok {
		return nil, "", time.Time{}, errors.New("the certificate request must hold an ECDSA key")
	}
	uri, _ := url.Parse(MachineURI(machineID))
	sn := serial()
	notAfter = now.Add(agentValidity)
	tmpl := &x509.Certificate{
		SerialNumber: sn, Subject: pkix.Name{CommonName: "enclave-agent " + machineID},
		NotBefore: now.Add(-clockSkew), NotAfter: notAfter, URIs: []*url.URL{uri},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, csr.PublicKey, ca.Key)
	if err != nil {
		return nil, "", time.Time{}, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemCertificate, Bytes: der}), sn.Text(16), notAfter, nil
}

// ControlPlaneCertificate issues the control plane's client certificate, valid 30 days, kept in memory.
func (ca *CA) ControlPlaneCertificate(now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: ControlPlaneCN},
		NotBefore: now.Add(-clockSkew), NotAfter: now.Add(controlPlaneValidity),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// NewKeyAndRequest makes an agent's key and its certificate request (the key never leaves the machine).
func NewKeyAndRequest() (keyPEM, csrPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "enclave-agent"}}, key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err = encodeKey(key)
	return keyPEM, pem.EncodeToMemory(&pem.Block{Type: pemCertificateRequest, Bytes: der}), err
}

// ServerConfig is the agent's TLS: its own certificate, and only the control plane's client certificate,
// chaining to the CA, may connect (TLS 1.3).
func ServerConfig(certPEM, keyPEM, caPEM []byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("the agent's certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("the CA certificate isn't PEM")
	}
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool,
		VerifyPeerCertificate: func(_ [][]byte, chains [][]*x509.Certificate) error {
			for _, chain := range chains {
				if len(chain) > 0 && chain[0].Subject.CommonName == ControlPlaneCN {
					return nil
				}
			}
			return errors.New("only the control plane may call this agent")
		},
	}, nil
}

// ClientConfig is the control plane's TLS towards one machine: its client certificate, and the agent's
// certificate must chain to the CA, name machineID and have the serial recorded for it.
func ClientConfig(cert tls.Certificate, caPEM []byte, machineID, serialHex string) (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("the CA certificate isn't PEM")
	}
	want := MachineURI(machineID)
	return &tls.Config{
		MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert},
		// Agents are reached by address, and their certificates carry their machine, not a host name: the
		// chain, the machine and the serial are checked below instead of the host name.
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("the agent sent no certificate")
			}
			leaf, err := x509.ParseCertificate(raw[0])
			if err != nil {
				return err
			}
			inter := x509.NewCertPool()
			for _, r := range raw[1:] {
				if c, err := x509.ParseCertificate(r); err == nil {
					inter.AddCert(c)
				}
			}
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
				return fmt.Errorf("the agent's certificate isn't from this EnclaveStack: %w", err)
			}
			if len(leaf.URIs) != 1 || leaf.URIs[0].String() != want {
				return fmt.Errorf("the agent's certificate is for another machine")
			}
			if serialHex != "" && leaf.SerialNumber.Text(16) != serialHex {
				return errors.New("the agent's certificate was replaced or revoked")
			}
			return nil
		},
	}, nil
}

// CertificateExpiry reads a PEM certificate's end of validity and serial.
func CertificateExpiry(certPEM []byte) (time.Time, string, error) {
	c, err := parseCert(certPEM)
	if err != nil {
		return time.Time{}, "", err
	}
	return c.NotAfter, c.SerialNumber.Text(16), nil
}

// ParseKey reads an EC private key.
func ParseKey(keyPEM []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != pemPrivateKey {
		return nil, errors.New("the key isn't PEM")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

func parseCert(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != pemCertificate {
		return nil, errors.New("the certificate isn't PEM")
	}
	return x509.ParseCertificate(block.Bytes)
}

func encodeKey(key *ecdsa.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemPrivateKey, Bytes: der}), nil
}

// serial is 128 random bits.
func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return n
}
