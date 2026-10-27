package machines

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/enclavestack/platform/internal/agent"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/internal/version"
)

// The standby control plane, main machine side (ADR-057): copies of the database and the master key are
// pushed to the standby's agent; every agent call carries the epoch, and a refusal stops this control plane.

// ErrReplaced is this control plane after a standby was promoted.
var ErrReplaced = errors.New("a promoted standby replaced this control plane")

// DefaultStandbyEvery is how often the main machine checks for changes to copy.
const DefaultStandbyEvery = 15 * time.Second

// StandbyConfig is where the control plane's files are.
type StandbyConfig struct {
	DBPath  string
	KeyPath string
	TempDir string
	Every   time.Duration
}

// fencing is the epoch the manager sends and whether an agent refused it.
type fencing struct {
	mu     sync.Mutex
	epoch  int64
	fenced bool
	once   sync.Once
}

func (m *Manager) currentEpoch() int64 {
	m.fence.mu.Lock()
	defer m.fence.mu.Unlock()
	return m.fence.epoch
}

// LoadEpoch reads the epoch from the store; called at start.
func (m *Manager) LoadEpoch(ctx context.Context) error {
	e, err := m.Store.Epoch(ctx)
	if err != nil {
		return err
	}
	m.fence.mu.Lock()
	m.fence.epoch = e
	m.fence.mu.Unlock()
	return nil
}

// Replaced reports whether an agent refused this control plane because a standby was promoted.
func (m *Manager) Replaced() bool {
	m.fence.mu.Lock()
	defer m.fence.mu.Unlock()
	return m.fence.fenced
}

func (m *Manager) fenced() {
	m.fence.mu.Lock()
	m.fence.fenced = true
	m.fence.mu.Unlock()
	m.fence.once.Do(func() {
		m.Log.Error("an agent refused this control plane: a standby was promoted and took over. Stopping.")
		if m.Fenced != nil {
			m.Fenced()
		}
	})
}

// fingerprint is the size and modification time of the database and its write-ahead log.
type fingerprint [4]int64

func fingerprintOf(dbPath string) fingerprint {
	var f fingerprint
	for i, p := range []string{dbPath, dbPath + "-wal"} {
		if st, err := os.Stat(p); err == nil {
			f[2*i], f[2*i+1] = st.Size(), st.ModTime().UnixNano()
		}
	}
	return f
}

// RunStandby copies the control plane to the standby whenever the database changed, until ctx ends.
func (m *Manager) RunStandby(ctx context.Context, cfg StandbyConfig) {
	every := orDefault(cfg.Every, DefaultStandbyEvery)
	t := time.NewTicker(every)
	defer t.Stop()
	var last fingerprint
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if m.Replaced() {
			return
		}
		before := fingerprintOf(cfg.DBPath)
		if before == last {
			continue
		}
		sent, changed, err := m.copyToStandby(ctx, cfg, before)
		if err != nil && ctx.Err() == nil {
			m.Log.Warn("copy to the standby failed", "error", err)
		}
		last = copiedState(last, sent, changed, err, func() fingerprint { return fingerprintOf(cfg.DBPath) })
	}
}

// copiedState is the database state the standby now holds, for the next tick to compare with. A write
// during the copy may be missing from it, so the next tick copies again; otherwise the state after the
// copy was recorded, so the record itself isn't a change. A failed attempt with no standby keeps last.
func copiedState(last fingerprint, sent, changed bool, err error, now func() fingerprint) fingerprint {
	switch {
	case changed:
		return fingerprint{}
	case sent || err == nil:
		return now()
	}
	return last
}

// CopyToStandby sends one copy to the standby, if there is one online, and records the attempt. It
// reports whether there was a standby to send to.
func (m *Manager) CopyToStandby(ctx context.Context, cfg StandbyConfig) (bool, error) {
	sent, _, err := m.copyToStandby(ctx, cfg, fingerprintOf(cfg.DBPath))
	return sent, err
}

// copyToStandby is CopyToStandby that also reports whether the database changed from before while the
// copy was made, which the copy may not hold.
func (m *Manager) copyToStandby(ctx context.Context, cfg StandbyConfig, before fingerprint) (sent, changed bool, err error) {
	all, err := m.Store.ListMachines(ctx)
	if err != nil {
		return false, false, err
	}
	var sb *store.Machine
	for i := range all {
		if all[i].IsStandby() && all[i].ID != m.Store.MainID() {
			sb = &all[i]
		}
	}
	if sb == nil || !m.Online(sb.ID) {
		return false, false, nil
	}
	n, takenAt, err := m.sendCopy(ctx, *sb, cfg)
	changed = fingerprintOf(cfg.DBPath) != before
	failure, at := "", takenAt // a copy holds the data as of when it was taken, not when it arrived
	if err != nil {
		failure, at = err.Error(), m.Now()
	}
	if rerr := m.Store.RecordStandbyCopy(ctx, sb.ID, n, failure, at); rerr != nil {
		return true, changed, errors.Join(err, rerr)
	}
	return true, changed, err
}

// sendCopy sends one copy and returns its size and when the database was copied.
func (m *Manager) sendCopy(ctx context.Context, sb store.Machine, cfg StandbyConfig) (int64, time.Time, error) {
	if err := os.MkdirAll(cfg.TempDir, 0o700); err != nil {
		return 0, time.Time{}, err
	}
	dir, err := os.MkdirTemp(cfg.TempDir, "standby-*")
	if err != nil {
		return 0, time.Time{}, err
	}
	defer os.RemoveAll(dir)
	db := filepath.Join(dir, agent.CopyDatabase)
	now := m.Now() // before the copy: every change committed by now is in it
	if err := m.Store.VacuumInto(ctx, db); err != nil {
		return 0, now, fmt.Errorf("copy the database: %w", err)
	}
	archive := filepath.Join(dir, "copy.tar.gz")
	f, err := os.OpenFile(archive, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, now, err
	}
	_, err = agent.WriteCopy(f, db, cfg.KeyPath, version.Version, m.Store.MainID(), m.currentEpoch(), now)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, now, fmt.Errorf("pack the copy: %w", err)
	}
	r, err := m.client(ctx, sb)
	if err != nil {
		return 0, now, err
	}
	sctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	c, err := r.client.SendStandbyCopy(sctx, archive, now)
	if err != nil {
		return 0, now, fmt.Errorf("send the copy to %s: %w", sb.Name, err)
	}
	return c.Bytes, now, nil
}

// Promotion is what a promotion did.
type Promotion struct {
	Machine string
	Epoch   int64
	TakenAt time.Time
}

// Promote turns a standby into the main machine (ADR-057 steps 1–2): it unpacks the newest good copy into
// dataDir and, in one transaction, makes this machine the main one and raises the epoch. The caller then
// starts the control plane on dataDir.
func Promote(ctx context.Context, stateDir, dataDir string, now time.Time) (Promotion, error) {
	id, err := agent.Load(stateDir)
	if err != nil {
		return Promotion{}, err
	}
	if !id.Standby {
		return Promotion{}, fmt.Errorf("the machine %s isn't a standby: only a standby holds copies of the control plane", id.Name)
	}
	man, err := agent.RestoreNewestCopy(stateDir, dataDir)
	if err != nil {
		return Promotion{}, err
	}
	st, err := store.Open(ctx, filepath.Join(dataDir, agent.CopyDatabase))
	if err != nil {
		return Promotion{}, err
	}
	defer st.Close()
	var epoch int64
	err = st.WithTx(ctx, func(q *store.Queries) error {
		var err error
		if epoch, err = q.Promote(ctx, id.MachineID, now); err != nil {
			return err
		}
		return q.AddMachineEvent(ctx, id.MachineID, "promoted", fmt.Sprintf("Promoted to the main machine from the copy taken %s.",
			man.TakenAt.UTC().Format("2 Jan 2006 15:04:05 UTC")), now)
	})
	if errors.Is(err, store.ErrNotFound) {
		return Promotion{}, fmt.Errorf("the copy doesn't know the machine %s: it was taken before this standby joined", id.Name)
	}
	return Promotion{Machine: id.Name, Epoch: epoch, TakenAt: man.TakenAt}, err
}
