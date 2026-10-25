package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/enclavestack/platform/pkg/api"
)

// Machine row (ADR-042 – ADR-044, L3-005 §10).
type Machine struct {
	ID              string
	Name            string
	State           string
	StateSince      time.Time
	Labels          map[string]string
	Schedulable     bool
	Address         string
	AgentPort       int
	TunnelPort      int
	TunnelPublicKey string
	CertSerial      string
	CertNotAfter    *time.Time
	Info            MachineInfo
	LastSeenAt      *time.Time
	JoinCodeHash    string
	JoinExpiresAt   *time.Time
	CreatedAt       time.Time
	// Main is the computer EnclaveStack runs on; exactly one machine is (MCH-01).
	Main bool
	// Role is api.MachineWorker or api.MachineEdge (L2-014 EDG-01).
	Role string
	// JoinKey is an edge machine's join key sealed with the master key, until it joins (ADR-052).
	JoinKey []byte
}

// MachineInfo is what a machine's agent last reported about it (or, for the main machine, its engine).
type MachineInfo struct {
	OS               string        `json:"os,omitempty"`
	Arch             string        `json:"arch,omitempty"`
	AgentVersion     string        `json:"agentVersion,omitempty"`
	Engine           api.Engine    `json:"engine"`
	CPUs             int           `json:"cpus,omitempty"`
	MemoryTotalBytes int64         `json:"memoryTotalBytes,omitempty"`
	VMs              api.VMSupport `json:"vms"`
}

// MainID is the main machine's ID ("" until EnsureMainMachine has run).
func (s *Store) MainID() string {
	id, _ := s.mainID.Load().(string)
	return id
}

// legacyMainID is the ID the main machine had before 0014: a fixed word instead of a made ID.
const legacyMainID = "main"

// EnsureMainMachine makes sure the main machine has a generated ID (mch_…) and a name made from the
// computer's own name, and returns its ID (MCH-01). A database from before 0014 has the row "main": it is
// renamed once, with everything that pointed at it, in one transaction. newID makes the ID; handle is the
// name wanted, which gets -2, -3… when another machine already has it.
func (s *Store) EnsureMainMachine(ctx context.Context, newID func() string, handle string, now time.Time) (string, error) {
	var id string
	err := s.WithTx(ctx, func(q *Queries) error {
		var main Machine
		var err error
		if main, err = q.oneMachine(ctx, `SELECT `+machineCols+` FROM machines WHERE is_main = 1`); errors.Is(err, ErrNotFound) {
			main, err = Machine{}, nil
		} else if err != nil {
			return err
		}
		switch {
		case main.ID != "" && main.ID != legacyMainID:
			id = main.ID // already done
			return nil
		case main.ID == "":
			// A new database has the flagged row from 0013 as "main"; with none, make one.
			main = Machine{ID: newID(), Name: handle, State: api.MachineOnline, StateSince: now, Schedulable: true, CreatedAt: now, Main: true}
			if main.Name, err = q.freeMachineName(ctx, handle, ""); err != nil {
				return err
			}
			id = main.ID
			return q.InsertMachine(ctx, main)
		}
		name, err := q.freeMachineName(ctx, handle, legacyMainID)
		if err != nil {
			return err
		}
		id = newID()
		return q.renameMainMachine(ctx, name, id)
	})
	if err != nil {
		return "", err
	}
	s.mainID.Store(id)
	return id, nil
}

// freeMachineName returns name, or name-2, name-3… when another machine (not ignore) already has it.
func (q *Queries) freeMachineName(ctx context.Context, name, ignoreID string) (string, error) {
	for n := 1; ; n++ {
		try := name
		if n > 1 {
			try = name + "-" + strconv.Itoa(n)
		}
		var taken int
		if err := q.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE name = ? AND id <> ?`, try, ignoreID).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return try, nil
		}
	}
}

// renameMainMachine gives the pre-0014 row "main" its generated ID and name, and rewrites everything that
// held the old ID or name: placements, address pools, events, and the machine name in specs and outputs.
func (q *Queries) renameMainMachine(ctx context.Context, name, id string) error {
	if _, err := q.q.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`); err != nil {
		return err
	}
	steps := []struct {
		sql  string
		args []any
	}{
		{`UPDATE machines SET id = ?, name = ? WHERE id = ?`, []any{id, name, legacyMainID}},
		{`UPDATE resources SET machine_id = ? WHERE machine_id = ?`, []any{id, legacyMainID}},
		{`UPDATE address_pool SET machine_id = ? WHERE machine_id = ?`, []any{id, legacyMainID}},
		{`UPDATE machine_events SET machine_id = ? WHERE machine_id = ?`, []any{id, legacyMainID}},
		// A pinned resource names its machine by name, and a placed one's outputs do too.
		{`UPDATE resources SET spec_json = json_set(spec_json, '$.machine', ?) WHERE json_extract(spec_json, '$.machine') = ?`, []any{name, legacyMainID}},
		{`UPDATE resources SET outputs_json = json_set(outputs_json, '$.machine', ?) WHERE json_extract(outputs_json, '$.machine') = ?`, []any{name, legacyMainID}},
	}
	for _, st := range steps {
		if _, err := q.q.ExecContext(ctx, st.sql, st.args...); err != nil {
			return fmt.Errorf("make the main machine %s: %w", name, err)
		}
	}
	return nil
}

const machineCols = `id, name, state, state_since, labels_json, schedulable, address, agent_port, tunnel_port, tunnel_public_key,
	cert_serial, cert_not_after, info_json, last_seen_at, join_code_hash, join_expires_at, created_at, is_main, role, join_key_ciphertext`

// InsertMachine stores a new machine; a duplicate name or join code returns ErrConflict.
func (q *Queries) InsertMachine(ctx context.Context, m Machine) error {
	labels, info, err := encodeMachine(m)
	if err != nil {
		return err
	}
	_, err = q.q.ExecContext(ctx, `INSERT INTO machines (`+machineCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Name, m.State, ts(m.StateSince), labels, boolInt(m.Schedulable), nullable(m.Address), m.AgentPort, m.TunnelPort, m.TunnelPublicKey,
		m.CertSerial, tsPtr(m.CertNotAfter), info, tsPtr(m.LastSeenAt), nullable(m.JoinCodeHash), tsPtr(m.JoinExpiresAt), ts(m.CreatedAt), boolInt(m.Main),
		roleOf(m), nullBytes(m.JoinKey))
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// SaveMachine writes every mutable column of m.
func (q *Queries) SaveMachine(ctx context.Context, m Machine) error {
	labels, info, err := encodeMachine(m)
	if err != nil {
		return err
	}
	res, err := q.q.ExecContext(ctx, `UPDATE machines SET state = ?, state_since = ?, labels_json = ?, schedulable = ?, address = ?, agent_port = ?,
		tunnel_port = ?, tunnel_public_key = ?, cert_serial = ?, cert_not_after = ?, info_json = ?, last_seen_at = ?, join_code_hash = ?,
		join_expires_at = ?, join_key_ciphertext = ? WHERE id = ?`,
		m.State, ts(m.StateSince), labels, boolInt(m.Schedulable), nullable(m.Address), m.AgentPort, m.TunnelPort, m.TunnelPublicKey,
		m.CertSerial, tsPtr(m.CertNotAfter), info, tsPtr(m.LastSeenAt), nullable(m.JoinCodeHash), tsPtr(m.JoinExpiresAt), nullBytes(m.JoinKey), m.ID)
	if isUnique(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SaveMachineHealth writes only what health checks own: state, facts, tunnel key, certificate and last
// seen. An admin's change to labels or schedulability made meanwhile stays (ADR-046).
func (q *Queries) SaveMachineHealth(ctx context.Context, m Machine) error {
	_, info, err := encodeMachine(m)
	if err != nil {
		return err
	}
	res, err := q.q.ExecContext(ctx, `UPDATE machines SET state = ?, state_since = ?, tunnel_public_key = ?, cert_serial = ?, cert_not_after = ?,
		info_json = ?, last_seen_at = ? WHERE id = ?`,
		m.State, ts(m.StateSince), m.TunnelPublicKey, m.CertSerial, tsPtr(m.CertNotAfter), info, tsPtr(m.LastSeenAt), m.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetMachine returns a machine by ID or ErrNotFound.
func (q *Queries) GetMachine(ctx context.Context, id string) (Machine, error) {
	return q.oneMachine(ctx, `SELECT `+machineCols+` FROM machines WHERE id = ?`, id)
}

// GetMachineByName returns a machine by name or ErrNotFound.
func (q *Queries) GetMachineByName(ctx context.Context, name string) (Machine, error) {
	return q.oneMachine(ctx, `SELECT `+machineCols+` FROM machines WHERE name = ?`, name)
}

// GetMachineByJoinCode returns the machine waiting with a code's hash, or ErrNotFound.
func (q *Queries) GetMachineByJoinCode(ctx context.Context, hash string) (Machine, error) {
	return q.oneMachine(ctx, `SELECT `+machineCols+` FROM machines WHERE join_code_hash = ?`, hash)
}

// ListMachines returns every machine, the main machine first, then by name.
func (q *Queries) ListMachines(ctx context.Context) ([]Machine, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT `+machineCols+` FROM machines ORDER BY is_main DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMachine removes a machine (its pool and events cascade); one with resources returns ErrConflict.
func (q *Queries) DeleteMachine(ctx context.Context, id string) error {
	res, err := q.q.ExecContext(ctx, `DELETE FROM machines WHERE id = ?`, id)
	if isForeignKey(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MachineResourceCounts counts resources per machine, in the given projects (nil: all).
func (q *Queries) MachineResourceCounts(ctx context.Context, projects []string) (map[string]int, error) {
	query, args := `SELECT COALESCE(machine_id, ''), COUNT(*) FROM resources`, []any{}
	if projects != nil {
		if len(projects) == 0 {
			return map[string]int{}, nil
		}
		query += ` WHERE project IN (` + placeholders(len(projects)) + `)`
		for _, p := range projects {
			args = append(args, p)
		}
	}
	rows, err := q.q.QueryContext(ctx, query+` GROUP BY 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// AddMachineEvent appends an event and keeps at most 200 per machine (L3-005 §10).
func (q *Queries) AddMachineEvent(ctx context.Context, machineID, typ, message string, at time.Time) error {
	if _, err := q.q.ExecContext(ctx, `INSERT INTO machine_events (machine_id, type, message, created_at) VALUES (?, ?, ?, ?)`, machineID, typ, message, ts(at)); err != nil {
		return err
	}
	_, err := q.q.ExecContext(ctx, `DELETE FROM machine_events WHERE machine_id = ? AND id NOT IN
		(SELECT id FROM machine_events WHERE machine_id = ? ORDER BY id DESC LIMIT 200)`, machineID, machineID)
	return err
}

// ListMachineEvents returns a machine's newest events first.
func (q *Queries) ListMachineEvents(ctx context.Context, machineID string, limit int) ([]api.Event, error) {
	rows, err := q.q.QueryContext(ctx, `SELECT id, type, message, created_at FROM machine_events WHERE machine_id = ? ORDER BY id DESC LIMIT ?`, machineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []api.Event{}
	for rows.Next() {
		var e api.Event
		var created string
		if err := rows.Scan(&e.ID, &e.Type, &e.Message, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MachineCA is the certificate authority that signs agents and the control plane (ADR-043).
type MachineCA struct {
	CertPEM       string
	KeyCiphertext []byte
	CreatedAt     time.Time
}

// GetMachineCA returns the CA, or ErrNotFound before the first machine is added.
func (q *Queries) GetMachineCA(ctx context.Context) (MachineCA, error) {
	var ca MachineCA
	var created string
	err := q.q.QueryRowContext(ctx, `SELECT cert_pem, key_ciphertext, created_at FROM machine_ca WHERE id = 1`).Scan(&ca.CertPEM, &ca.KeyCiphertext, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return ca, ErrNotFound
	}
	ca.CreatedAt = parseTS(created)
	return ca, err
}

// PutMachineCA stores the CA once; a second one returns ErrConflict.
func (q *Queries) PutMachineCA(ctx context.Context, ca MachineCA) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO machine_ca (id, cert_pem, key_ciphertext, created_at) VALUES (1, ?, ?, ?)`, ca.CertPEM, ca.KeyCiphertext, ts(ca.CreatedAt))
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// PoolAddress is one address of a machine's pool in an engine network (ADR-045).
type PoolAddress struct {
	Network   string
	Address   string
	MachineID string
}

// AddressPool returns every pooled address of a network, by address.
func (q *Queries) AddressPool(ctx context.Context, network string) ([]PoolAddress, error) {
	return q.pool(ctx, `SELECT network, address, machine_id FROM address_pool WHERE network = ? ORDER BY address`, network)
}

// AllAddressPools returns every pooled address.
func (q *Queries) AllAddressPools(ctx context.Context) ([]PoolAddress, error) {
	return q.pool(ctx, `SELECT network, address, machine_id FROM address_pool ORDER BY network, address`)
}

// PoolAddressTo gives an address to a machine; an address another machine has returns ErrConflict.
func (q *Queries) PoolAddressTo(ctx context.Context, network, address, machineID string) error {
	res, err := q.q.ExecContext(ctx, `INSERT INTO address_pool (network, address, machine_id) VALUES (?, ?, ?)
		ON CONFLICT(network, address) DO UPDATE SET machine_id = excluded.machine_id WHERE address_pool.machine_id = excluded.machine_id`, network, address, machineID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrConflict
	}
	return nil
}

// MovePoolAddress hands an address from one machine's pool to another's (a move keeps its address, ADR-047).
func (q *Queries) MovePoolAddress(ctx context.Context, network, address, toMachine string) error {
	_, err := q.q.ExecContext(ctx, `INSERT INTO address_pool (network, address, machine_id) VALUES (?, ?, ?)
		ON CONFLICT(network, address) DO UPDATE SET machine_id = excluded.machine_id`, network, address, toMachine)
	return err
}

// DropNetworkPool removes a deleted network's addresses.
func (q *Queries) DropNetworkPool(ctx context.Context, network string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM address_pool WHERE network = ?`, network)
	return err
}

func (q *Queries) pool(ctx context.Context, query string, args ...any) ([]PoolAddress, error) {
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PoolAddress
	for rows.Next() {
		var p PoolAddress
		if err := rows.Scan(&p.Network, &p.Address, &p.MachineID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (q *Queries) oneMachine(ctx context.Context, query string, args ...any) (Machine, error) {
	rows, err := q.q.QueryContext(ctx, query, args...)
	if err != nil {
		return Machine{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Machine{}, err
		}
		return Machine{}, ErrNotFound
	}
	return scanMachine(rows)
}

func scanMachine(r scanner) (Machine, error) {
	var m Machine
	var since, labels, info, created string
	var schedulable, isMain int
	var address, certNotAfter, lastSeen, joinHash, joinExpires sql.NullString
	err := r.Scan(&m.ID, &m.Name, &m.State, &since, &labels, &schedulable, &address, &m.AgentPort, &m.TunnelPort, &m.TunnelPublicKey,
		&m.CertSerial, &certNotAfter, &info, &lastSeen, &joinHash, &joinExpires, &created, &isMain, &m.Role, &m.JoinKey)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal([]byte(labels), &m.Labels); err != nil {
		return m, err
	}
	if err := json.Unmarshal([]byte(info), &m.Info); err != nil {
		return m, err
	}
	m.StateSince, m.CreatedAt, m.Schedulable, m.Main = parseTS(since), parseTS(created), schedulable != 0, isMain != 0
	m.Address, m.JoinCodeHash = address.String, joinHash.String
	m.CertNotAfter, m.LastSeenAt, m.JoinExpiresAt = parseNullTS(certNotAfter), parseNullTS(lastSeen), parseNullTS(joinExpires)
	return m, nil
}

func encodeMachine(m Machine) (string, string, error) {
	labels := m.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	lb, err := json.Marshal(labels)
	if err != nil {
		return "", "", err
	}
	ib, err := json.Marshal(m.Info)
	return string(lb), string(ib), err
}

// roleOf is m's role, a worker unless it says otherwise.
func roleOf(m Machine) string {
	if m.Role == "" {
		return api.MachineWorker
	}
	return m.Role
}

// IsEdge reports whether m only relays visitors (L2-014 EDG-03).
func (m Machine) IsEdge() bool { return m.Role == api.MachineEdge }

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
