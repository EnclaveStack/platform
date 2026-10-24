package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Public access (L3-005 §11, ADR-051 – ADR-053): each tunnel's edge listener ports, and the gate's sessions.

// First listener ports: app links and Cloudflare tunnels from 11000, relays (a pair each) from 12000.
const (
	TunnelPortBase = 11000
	RelayPortBase  = 12000
)

// SettingGateEdgeKey is the sealed key the edge proxy shows the gate listener (ADR-053).
const SettingGateEdgeKey = "gate_edge_key"

// TunnelPorts are a tunnel's edge listeners. HTTPSPort is 0 except for a relay.
type TunnelPorts struct {
	TunnelID  string
	HTTPPort  int
	HTTPSPort int
}

// EnsureTunnelPorts returns a tunnel's listener ports, choosing free ones the first time. They stay for the
// tunnel's life, so the edge configuration never shifts under another tunnel (ADR-051).
func (q *Queries) EnsureTunnelPorts(ctx context.Context, tunnelID string, relay bool) (TunnelPorts, error) {
	if p, err := q.GetTunnelPorts(ctx, tunnelID); err == nil {
		return p, nil
	} else if !errors.Is(err, ErrNotFound) {
		return TunnelPorts{}, err
	}
	used := map[int]bool{}
	rows, err := q.q.QueryContext(ctx, `SELECT http_port, COALESCE(https_port, 0) FROM tunnel_ports`)
	if err != nil {
		return TunnelPorts{}, err
	}
	for rows.Next() {
		var h, s int
		if err := rows.Scan(&h, &s); err != nil {
			rows.Close()
			return TunnelPorts{}, err
		}
		used[h], used[s] = true, true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return TunnelPorts{}, err
	}
	p := TunnelPorts{TunnelID: tunnelID}
	if relay {
		for port := RelayPortBase; ; port += 2 {
			if !used[port] && !used[port+1] {
				p.HTTPPort, p.HTTPSPort = port, port+1
				break
			}
		}
	} else {
		for port := TunnelPortBase; ; port++ {
			if !used[port] {
				p.HTTPPort = port
				break
			}
		}
	}
	var https any
	if p.HTTPSPort != 0 {
		https = p.HTTPSPort
	}
	if _, err := q.q.ExecContext(ctx, `INSERT INTO tunnel_ports (tunnel_id, http_port, https_port) VALUES (?, ?, ?)`, tunnelID, p.HTTPPort, https); err != nil {
		if isUnique(err) {
			return TunnelPorts{}, ErrConflict
		}
		return TunnelPorts{}, err
	}
	return p, nil
}

// GetTunnelPorts returns a tunnel's listener ports or ErrNotFound.
func (q *Queries) GetTunnelPorts(ctx context.Context, tunnelID string) (TunnelPorts, error) {
	p := TunnelPorts{TunnelID: tunnelID}
	var https sql.NullInt64
	err := q.q.QueryRowContext(ctx, `SELECT http_port, https_port FROM tunnel_ports WHERE tunnel_id = ?`, tunnelID).Scan(&p.HTTPPort, &https)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.HTTPSPort = int(https.Int64)
	return p, err
}

// GateSession is one visitor's sign-in through a gate, for one hostname (ADR-053). Only the token's hash is kept.
type GateSession struct {
	TokenHash string
	UserID    string
	Hostname  string
	CreatedAt time.Time
	ExpiresAt time.Time
	CheckedAt time.Time
}

// InsertGateSession stores a new gate session.
func (q *Queries) InsertGateSession(ctx context.Context, g GateSession) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO gate_sessions (token_hash, user_id, hostname, created_at, expires_at, checked_at) VALUES (?, ?, ?, ?, ?, ?)`,
		g.TokenHash, g.UserID, g.Hostname, ts(g.CreatedAt), ts(g.ExpiresAt), ts(g.CheckedAt))
	return err
}

// GetGateSession returns a session by its token's hash, or ErrNotFound.
func (q *Queries) GetGateSession(ctx context.Context, hash string) (GateSession, error) {
	g := GateSession{TokenHash: hash}
	var created, expires, checked string
	err := q.q.QueryRowContext(ctx, `SELECT user_id, hostname, created_at, expires_at, checked_at FROM gate_sessions WHERE token_hash = ?`, hash).
		Scan(&g.UserID, &g.Hostname, &created, &expires, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	g.CreatedAt, g.ExpiresAt, g.CheckedAt = parseTS(created), parseTS(expires), parseTS(checked)
	return g, err
}

// TouchGateSession records that a session's access was checked again at at.
func (q *Queries) TouchGateSession(ctx context.Context, hash string, at time.Time) error {
	_, err := q.q.ExecContext(ctx, `UPDATE gate_sessions SET checked_at = ? WHERE token_hash = ?`, ts(at), hash)
	return err
}

// DeleteGateSession ends one session.
func (q *Queries) DeleteGateSession(ctx context.Context, hash string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM gate_sessions WHERE token_hash = ?`, hash)
	return err
}

// DeleteExpiredGateSessions removes sessions past expiry and returns how many.
func (q *Queries) DeleteExpiredGateSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := q.q.ExecContext(ctx, `DELETE FROM gate_sessions WHERE expires_at <= ?`, ts(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
