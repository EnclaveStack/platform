package reconciler

import (
	"context"
	"errors"
	"fmt"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Networks and placement (ADR-020, L2-006 NET-01 – NET-08).

// ensureNetwork creates the engine network and connects EnclaveStack's own services to it.
func (r *Reconciler) ensureNetwork(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindNetwork, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	report.Step("network", api.StepActive)
	out, err := r.Driver.EnsureNetwork(ctx, engine.NetworkInput{Resource: res, Spec: raw.(api.NetworkSpec)})
	if err != nil {
		return nil, err
	}
	report.Step("network", api.StepDone)
	report.Step("attach", api.StepDone)
	return out, nil
}

// placement resolves a spec's network and subnet into where the engine places containers: the engine
// network, and a subnet range unless the subnet is the network's first (its automatic range).
func (r *Reconciler) placement(ctx context.Context, project, network, subnet string) (engine.Placement, error) {
	if network == "" {
		return engine.Placement{}, nil
	}
	n, err := r.Store.GetResourceByName(ctx, project, api.KindNetwork, network)
	if errors.Is(err, store.ErrNotFound) {
		return engine.Placement{}, engine.Permanent(fmt.Errorf("the network %s no longer exists", network))
	}
	if err != nil {
		return engine.Placement{}, err
	}
	raw, _, err := api.NormalizeSpec(api.KindNetwork, n.Spec)
	if err != nil {
		return engine.Placement{}, engine.Permanent(fmt.Errorf("the network %s has an unreadable spec", network))
	}
	if n.ObservedGeneration == 0 {
		return engine.Placement{}, fmt.Errorf("the network %s isn't ready yet", network)
	}
	subnets := raw.(api.NetworkSpec).EffectiveSubnets()
	p := engine.Placement{Network: engine.DockerNetwork(project, network)}
	for i, s := range subnets {
		if s.Name == subnet && i > 0 {
			p.Subnet = s.CIDR
		}
	}
	return p, nil
}

// placementOf returns a placed spec's network and subnet.
func placementOf(spec any) (network, subnet string) {
	switch s := spec.(type) {
	case api.AppSpec:
		return s.Network, s.Subnet
	case api.DatabaseSpec:
		return s.Network, s.Subnet
	case api.CacheSpec:
		return s.Network, s.Subnet
	case api.JobSpec:
		return s.Network, s.Subnet
	case api.SearchSpec:
		return s.Network, s.Subnet
	case api.EmailSpec:
		return s.Network, s.Subnet
	}
	return "", ""
}
