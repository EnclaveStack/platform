package fake

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// The fake keeps a database's or cache's "data" as bytes: Dump writes them, Restore replaces them, so
// tests can prove a restore brings back exactly what was backed up.

// SetData sets a resource's data.
func (d *Driver) SetData(resourceID, data string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.data == nil {
		d.data = map[string]string{}
	}
	d.data[resourceID] = data
}

// Data returns a resource's data.
func (d *Driver) Data(resourceID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.data[resourceID]
}

// Dump implements engine.Driver.
func (d *Driver) Dump(ctx context.Context, in engine.EnsureInput, w io.Writer) (engine.DumpInfo, error) {
	if err := d.Ping(ctx); err != nil {
		return engine.DumpInfo{}, err
	}
	r := in.Resource
	d.mu.Lock()
	f := d.faults[r.Name]
	rt := d.runtimes[r.ID]
	data := d.data[r.ID]
	d.mu.Unlock()
	if f != nil && f.DumpFails {
		return engine.DumpInfo{}, errors.New("pg_dump: connection lost (injected fault)")
	}
	if rt == nil || !rt.running {
		return engine.DumpInfo{}, fmt.Errorf("%s isn't running", r.Name)
	}
	info := engine.DumpInfo{Ext: engine.DumpExt(in.Spec), Engine: "redis (fake)"}
	if s, ok := in.Spec.(api.DatabaseSpec); ok {
		info.Engine = s.Engine + " (fake)"
	}
	if _, err := io.WriteString(w, "FAKEDUMP\n"+data); err != nil {
		return engine.DumpInfo{}, err
	}
	return info, nil
}

// Restore implements engine.Driver.
func (d *Driver) Restore(ctx context.Context, in engine.EnsureInput, src io.Reader) error {
	if _, err := d.Ensure(ctx, in, noReport{}); err != nil {
		return err
	}
	b, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	const header = "FAKEDUMP\n"
	if len(b) < len(header) || string(b[:len(header)]) != header {
		return engine.Permanent(errors.New("pg_restore: input file does not appear to be a valid archive (fake)"))
	}
	d.SetData(in.Resource.ID, string(b[len(header):]))
	return nil
}

type noReport struct{}

func (noReport) Step(string, api.StepState) {}

// The data a move copies (ADR-047): the same bytes, framed so a stray stream is refused.
const dataHeader = "FAKEDATA\n"

// StopResource implements engine.Driver: the runtime stays, not running, with its data.
func (d *Driver) StopResource(ctx context.Context, r *domain.Resource) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if rt := d.runtimes[r.ID]; rt != nil {
		rt.running = false
	}
	return nil
}

// ExportData implements engine.Driver.
func (d *Driver) ExportData(ctx context.Context, r *domain.Resource, w io.Writer) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	_, err := io.WriteString(w, dataHeader+d.Data(r.ID))
	return err
}

// ImportData implements engine.Driver.
func (d *Driver) ImportData(ctx context.Context, r *domain.Resource, src io.Reader) error {
	if err := d.Ping(ctx); err != nil {
		return err
	}
	b, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(b), dataHeader) {
		return engine.Permanent(errors.New("the copied data isn't a volume (fake)"))
	}
	d.SetData(r.ID, strings.TrimPrefix(string(b), dataHeader))
	return nil
}
