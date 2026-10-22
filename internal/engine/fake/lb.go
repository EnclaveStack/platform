package fake

import (
	"context"
	"fmt"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/lb"
	"github.com/enclavestack/platform/pkg/api"
)

// Load balancers (ADR-022): the fake records each configuration and reports every replica of its
// targets as healthy, unless a test marks some unhealthy.

// LoadBalancerConfig returns the configuration last applied to a load balancer.
func (d *Driver) LoadBalancerConfig(resourceID string) *lb.Config {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lbs[resourceID]
}

// SetUnhealthyTargets makes a load balancer report n of its replicas failing their checks.
func (d *Driver) SetUnhealthyTargets(name string, n int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lbUnhealthy[name] = n
}

// LoadBalancerStatus implements engine.Driver.
func (d *Driver) LoadBalancerStatus(ctx context.Context, r *domain.Resource) (lb.Status, error) {
	if err := d.Ping(ctx); err != nil {
		return lb.Status{}, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lbStatus(r), nil
}

// lbStatus counts the replicas of a load balancer's targets. Callers hold d.mu.
func (d *Driver) lbStatus(r *domain.Resource) lb.Status {
	cfg := d.lbs[r.ID]
	if cfg == nil {
		return lb.Status{}
	}
	var st lb.Status
	for _, t := range cfg.Targets {
		st.Targets += d.replicasByAlias[t.Host]
	}
	st.Healthy = max(st.Targets-d.lbUnhealthy[r.Name], 0)
	return st
}

// lbOutputs mirrors the Docker driver's load balancer outputs. Callers hold d.mu.
func (d *Driver) lbOutputs(r *domain.Resource, in engine.EnsureInput, rt *runtime) (engine.Outputs, error) {
	if in.LoadBalancer == nil {
		return nil, engine.Permanent(fmt.Errorf("the load balancer %s has no resolved targets", r.Name))
	}
	spec, _ := in.Spec.(api.LoadBalancerSpec)
	cfg := *in.LoadBalancer
	d.lbs[r.ID] = &cfg
	out := engine.Outputs{"internalHost": engine.ContainerName(r), "internalPort": spec.Port,
		engine.OutputPrivateIP: rt.ip, engine.OutputPrivateDNS: engine.PrivateDNS(r)}
	if spec.Publish {
		out["host"], out["port"] = "127.0.0.1", rt.port
	}
	st := d.lbStatus(r)
	out[engine.OutputTargets], out[engine.OutputHealthyTargets] = st.Targets, st.Healthy
	return out, nil
}
