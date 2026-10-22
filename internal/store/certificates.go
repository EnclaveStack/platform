package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Certificates obtained through DNS-01 (L3-005 §6, ADR-024). Keys are stored as secretbox ciphertext,
// sealed by the caller with the hostname as additional data.

// Certificate is one stored certificate.
type Certificate struct {
	Hostname, DomainID, ChainPEM, Issuer string
	KeyCiphertext                        []byte
	NotBefore, NotAfter, UpdatedAt       time.Time
}

const certCols = `hostname, domain_id, chain_pem, key_ciphertext, issuer, not_before, not_after, updated_at`

// PutCertificate stores or replaces a hostname's certificate.
func (q *Queries) PutCertificate(ctx context.Context, c *Certificate) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO certificates (`+certCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(hostname) DO UPDATE SET domain_id = excluded.domain_id, chain_pem = excluded.chain_pem, key_ciphertext = excluded.key_ciphertext,
		issuer = excluded.issuer, not_before = excluded.not_before, not_after = excluded.not_after, updated_at = excluded.updated_at`,
		c.Hostname, c.DomainID, c.ChainPEM, c.KeyCiphertext, c.Issuer, ts(c.NotBefore), ts(c.NotAfter), ts(c.UpdatedAt))
	return err
}

func scanCert(row interface{ Scan(...any) error }) (*Certificate, error) {
	var c Certificate
	var nb, na, up string
	if err := row.Scan(&c.Hostname, &c.DomainID, &c.ChainPEM, &c.KeyCiphertext, &c.Issuer, &nb, &na, &up); err != nil {
		return nil, err
	}
	c.NotBefore, c.NotAfter, c.UpdatedAt = parseTS(nb), parseTS(na), parseTS(up)
	return &c, nil
}

// GetCertificate returns a hostname's certificate, or ErrNotFound.
func (q *Queries) GetCertificate(ctx context.Context, hostname string) (*Certificate, error) {
	c, err := scanCert(q.q.QueryRowContext(ctx, `SELECT `+certCols+` FROM certificates WHERE hostname = ?`, hostname))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return c, err
}

// AllCertificates lists every stored certificate, by hostname.
func (q *Queries) AllCertificates(ctx context.Context) ([]*Certificate, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT `+certCols+` FROM certificates ORDER BY hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Certificate
	for rows.Next() {
		c, err := scanCert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteCertificate removes a hostname's certificate.
func (q *Queries) DeleteCertificate(ctx context.Context, hostname string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM certificates WHERE hostname = ?`, hostname)
	return err
}
