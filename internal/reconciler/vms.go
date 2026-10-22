package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// VMs (ADR-025, ADR-026): the driver keeps the VM's link container in its network, then the hypervisor
// converges the VM itself, its NIC streaming to the link's loopback port.

// VM outputs (L3-002 §8.2).
const (
	OutputSSHUser     = "sshUser"
	OutputImageSHA256 = "imageSha256"
	// SSHUser is the user created on every VM (VM-03).
	SSHUser = "enclave"
)

func (r *Reconciler) ensureVM(ctx context.Context, res *domain.Resource, rep engine.Reporter, restart bool) (engine.Outputs, error) {
	if err := r.hypervisorReady(ctx); err != nil {
		return nil, err
	}
	raw, _, err := api.NormalizeSpec(api.KindVM, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	spec := raw.(api.VMSpec)
	keys, err := r.vmKeys(ctx, res.Project, spec)
	if err != nil {
		return nil, err
	}
	image, err := r.vmImage(ctx, res, spec)
	if err != nil {
		return nil, err
	}
	disks, err := r.vmDisks(ctx, res.Project, spec)
	if err != nil {
		return nil, err
	}

	in := engine.EnsureInput{Resource: res, Spec: spec, HostPorts: linkPorts(res.Outputs)}
	if in.Placement, err = r.placement(ctx, res.Project, spec.Network, spec.Subnet); err != nil {
		return nil, err
	}
	if in.Firewall, err = r.policyFor(ctx, res); err != nil {
		return nil, err
	}
	in.Started = func(ctx context.Context) error { return r.syncFirewallsNaming(ctx, res) }
	rep.Step("network", api.StepActive)
	out, err := r.Driver.EnsureVMLink(ctx, in)
	if err != nil {
		return nil, err
	}
	port, _ := asInt64(out[engine.OutputLinkPort])
	st, err := r.Hypervisor.EnsureVM(ctx, engine.VMInput{ID: res.ID, Name: domain.Qualified(res.Project, res.Name), Spec: spec, SSHKeys: keys, Image: image, Disks: disks,
		Link: engine.VMLink{Host: "127.0.0.1", Port: int(port)}, Restart: restart}, rep)
	if err != nil {
		return nil, err
	}
	if api.SnapshotImage(spec.Image) != "" { // the root disk is a copy now: the snapshot may go (SNP-06)
		if err := r.Store.WithTx(ctx, func(q *store.Queries) error { return q.DropRef(ctx, res.ID, "spec.image") }); err != nil {
			return nil, err
		}
	}
	if st.Started {
		r.Metrics.VMStarts.WithLabelValues(st.Reason).Inc()
		r.event(ctx, res, "vm_started", map[string]string{
			"create": "Created and started.", "start": "Started.", "restart": "Restarted.",
			"config": "Restarted to apply the new CPUs, memory or disks.", "crash": "Started again after its process stopped unexpectedly.",
		}[st.Reason])
	}
	out[engine.OutputPower] = api.PowerStopped
	if st.Running {
		out[engine.OutputPower] = api.PowerRunning
	}
	out[OutputSSHUser] = SSHUser
	if st.ImageSHA256 != "" {
		out[OutputImageSHA256] = st.ImageSHA256
	} else if sha, ok := res.Outputs[OutputImageSHA256]; ok {
		out[OutputImageSHA256] = sha
	}
	return out, nil
}

// linkPorts are the host ports a VM's link keeps (ADR-026): QEMU's frame port and SSH.
func linkPorts(outputs map[string]any) map[int]int {
	ports := map[int]int{}
	if p, ok := asInt64(outputs[engine.OutputLinkPort]); ok {
		ports[engine.VMLinkFramePort] = int(p)
	}
	if addr, ok := outputs[engine.OutputSSHAddress].(string); ok {
		if _, p, err := net.SplitHostPort(addr); err == nil {
			if n, err := strconv.Atoi(p); err == nil {
				ports[api.SSHPort] = n
			}
		}
	}
	return ports
}

// vmKeys returns the SSH keys, reading a key secret (VM-03).
func (r *Reconciler) vmKeys(ctx context.Context, project string, spec api.VMSpec) ([]string, error) {
	if spec.SSHKeySecret == "" {
		return spec.SSHKeys, nil
	}
	v, err := r.secretValue(ctx, project, spec.SSHKeySecret)
	if err != nil {
		return nil, err
	}
	keys := api.SplitKeys(v)
	if errs := api.ValidateSSHKeys(keys, "keys"); len(errs) > 0 {
		return nil, engine.Permanent(fmt.Errorf("the secret %s doesn't hold valid SSH public keys: %s", spec.SSHKeySecret, errs[0].Message))
	}
	return keys, nil
}

// vmImage resolves where a VM's root disk comes from. A snapshot is needed only until the root disk
// exists (IMG-04, SNP-06).
func (r *Reconciler) vmImage(ctx context.Context, res *domain.Resource, spec api.VMSpec) (engine.ImageSource, error) {
	name := api.SnapshotImage(spec.Image)
	switch {
	case name != "":
		snap, err := r.Store.GetResourceByName(ctx, res.Project, api.KindSnapshot, name)
		if errors.Is(err, store.ErrNotFound) {
			if res.Outputs[engine.OutputPower] != nil { // created before the snapshot went
				return engine.ImageSource{}, nil
			}
			return engine.ImageSource{}, engine.Permanent(fmt.Errorf("the snapshot %s was deleted before %s was created from it", name, res.Name))
		}
		if err != nil {
			return engine.ImageSource{}, err
		}
		file := r.snapshotFile(snap, api.VolumeDisk)
		if _, err := os.Stat(file); err != nil {
			return engine.ImageSource{}, engine.Permanent(fmt.Errorf("the snapshot %s's file is missing from %s", name, r.SnapshotDir))
		}
		return engine.ImageSource{SnapshotFile: file}, nil
	case api.IsURLImage(spec.Image):
		return engine.ImageSource{URL: spec.Image, SHA256: spec.ImageSHA256}, nil
	}
	return engine.ImageSource{Catalog: spec.Image}, nil
}

// vmDisks resolves a VM's disk volumes; each must be ready (VOL-01).
func (r *Reconciler) vmDisks(ctx context.Context, project string, spec api.VMSpec) ([]engine.DiskAttachment, error) {
	var out []engine.DiskAttachment
	for _, name := range spec.Disks {
		vol, err := r.Store.GetResourceByName(ctx, project, api.KindVolume, name)
		if errors.Is(err, store.ErrNotFound) {
			return nil, engine.Permanent(fmt.Errorf("the disk volume %s no longer exists", name))
		}
		if err != nil {
			return nil, err
		}
		if vol.ObservedGeneration == 0 {
			return nil, fmt.Errorf("waiting for the disk volume %s to be created", name)
		}
		out = append(out, engine.DiskAttachment{DiskID: vol.ID, Serial: api.DiskSerial(name)})
	}
	return out, nil
}

// deleteVM stops the VM, removes its directory and its link (VM-12); its data disks are kept.
func (r *Reconciler) deleteVM(ctx context.Context, res *domain.Resource) error {
	if err := r.hypervisorReady(ctx); err != nil && res.Outputs[engine.OutputPower] != nil {
		return err // it ran here once: its files need QEMU's tools to go
	}
	if r.Hypervisor != nil {
		if err := r.Hypervisor.DeleteVM(ctx, res.ID); err != nil {
			return err
		}
	}
	return r.Driver.DeleteVMLink(ctx, res)
}

// refreshVM is the drift pass's look at a ready VM (CP-10, VM-07): a guest that powered itself off is
// recorded as stopped, a crashed process or a missing link is restored.
func (r *Reconciler) refreshVM(ctx context.Context, res *domain.Resource, log *slog.Logger) {
	if r.Hypervisor == nil || !r.Hypervisor.Check(ctx).Available {
		return
	}
	raw, _, err := api.NormalizeSpec(api.KindVM, res.Spec)
	if err != nil {
		return
	}
	spec := raw.(api.VMSpec)
	if spec.Power != api.PowerRunning {
		return
	}
	st, err := r.Hypervisor.VMStatus(ctx, res.ID)
	if err != nil {
		return
	}
	linkOK := true
	if h, err := r.Driver.Health(ctx, res, engine.ObjectStore{}); err == nil && h != engine.Healthy {
		linkOK = false
	}
	switch {
	case st == engine.VMGuestShutdown:
		r.recordGuestShutdown(ctx, res, log)
	case st == engine.VMMissing || !linkOK:
		why := "Its process stopped unexpectedly; starting it again."
		if st == engine.VMRunning {
			why = "Its network link is missing; restoring it."
		} else {
			r.Metrics.VMStarts.WithLabelValues("crash").Inc()
		}
		if err := res.SetPhase(api.PhaseProvisioning, why, r.Now()); err != nil {
			return
		}
		res.Steps, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), r.Now()
		if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
			r.event(ctx, res, "drift_detected", why)
			r.Enqueue(res.ID)
		}
	}
}

// recordGuestShutdown sets power: stopped through the normal update path (a new generation), so the VM
// stays off and clients holding the old version see the change (VM-07, L3-005 §7).
func (r *Reconciler) recordGuestShutdown(ctx context.Context, res *domain.Resource, log *slog.Logger) {
	err := r.Store.WithTx(ctx, func(q *store.Queries) error {
		cur, err := q.GetResource(ctx, res.ID)
		if err != nil || cur.Generation != res.Generation {
			return err
		}
		raw, _, err := api.NormalizeSpec(api.KindVM, cur.Spec)
		if err != nil {
			return err
		}
		spec := raw.(api.VMSpec)
		spec.Power = api.PowerStopped
		b, err := json.Marshal(spec)
		if err != nil {
			return err
		}
		now := r.Now()
		cur.Spec, cur.Generation = b, cur.Generation+1
		if err := cur.SetPhase(api.PhaseUpdating, "The VM shut itself down.", now); err != nil {
			return err
		}
		cur.Steps, cur.Attempts, cur.NextAttemptAt, cur.UpdatedAt = domain.NewSteps(domain.StepNames[cur.Kind]...), 0, nil, now
		if err := q.SaveResource(ctx, cur); err != nil {
			return err
		}
		return q.AddEvent(ctx, cur.ID, "guest_shutdown", "The VM shut itself down (poweroff inside it), so it stays stopped. Start it to run it again.", now)
	})
	if err != nil {
		log.Warn("record a guest shutdown failed; the next drift pass retries", "resource_id", res.ID, "error", err)
		return
	}
	r.Enqueue(res.ID)
}
