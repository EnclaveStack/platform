package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// CDNs (ADR-033, L2-008 CDN-*) and email relays (ADR-032).

// cdnOrigin resolves where a CDN fetches from and the network its cache joins: an app's stable alias in
// the app's network, or the object store with the bucket as the path.
func (r *Reconciler) cdnOrigin(ctx context.Context, project string, s api.CDNSpec) (*engine.CDNConfig, engine.Placement, error) {
	kind, name, _ := api.ParseOrigin(s.Origin)
	cfg := &engine.CDNConfig{TTLSeconds: s.TTL(), MaxSizeMiB: s.MaxSizeMiB}
	origin, err := r.Store.GetResourceByName(ctx, project, kind, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, engine.Placement{}, engine.Permanent(fmt.Errorf("the %s %s no longer exists", kind, name))
	}
	if err != nil {
		return nil, engine.Placement{}, err
	}
	if kind == api.KindBucket {
		hostPort := net.JoinHostPort(engine.ObjectStoreName, strconv.Itoa(engine.ObjectStorePort))
		cfg.Upstream, cfg.PathPrefix, cfg.HostHeader = hostPort, "/"+name, hostPort
		return cfg, engine.Placement{}, nil
	}
	raw, _, err := api.NormalizeSpec(api.KindApp, origin.Spec)
	if err != nil {
		return nil, engine.Placement{}, engine.Permanent(fmt.Errorf("the app %s has an unreadable spec", name))
	}
	app := raw.(api.AppSpec)
	port := s.OriginPort
	if port == 0 {
		port = app.Port
	}
	if port == 0 {
		return nil, engine.Placement{}, engine.Permanent(fmt.Errorf("the app %s has no port; set the CDN's origin port", name))
	}
	cfg.Upstream, cfg.OriginIsApp = net.JoinHostPort(engine.ContainerName(origin), strconv.Itoa(port)), true
	placement, err := r.placement(ctx, project, app.Network, app.Subnet)
	return cfg, placement, err
}

// ensureCDN runs the cache, makes a bucket origin readable, then routes the hostname (CDN-01 – CDN-07).
func (r *Reconciler) ensureCDN(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindCDN, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	spec := raw.(api.CDNSpec)
	cfg, placement, err := r.cdnOrigin(ctx, res.Project, spec)
	if err != nil {
		return nil, err
	}
	kind, name, _ := api.ParseOrigin(spec.Origin)
	if kind == api.KindBucket {
		os, err := r.objectStore(ctx)
		if err != nil {
			return nil, err
		}
		if err := r.Driver.SetBucketPublicRead(ctx, name, true, os); err != nil {
			return nil, err
		}
	}
	in := engine.EnsureInput{Resource: res, Spec: spec, Placement: placement, CDN: cfg}
	out, err := r.Driver.Ensure(ctx, in, report)
	if err != nil {
		return nil, err
	}
	var targets []string
	if kind == api.KindApp {
		targets = []string{"app/" + name} // the app's firewall lets the cache in (FW-05)
	}
	host, err := r.serveHostname(ctx, res, spec.AsDomain(), targets, report)
	if err != nil {
		return nil, err
	}
	for k, v := range host {
		out[k] = v
	}
	if n, err := r.Driver.CacheUsage(ctx, res); err == nil {
		out[engine.OutputCachedBytes] = n
	}
	return out, nil
}

// deleteCDN removes the cache, its route, and a bucket's public reads when no other CDN serves it.
func (r *Reconciler) deleteCDN(ctx context.Context, res *domain.Resource) error {
	if err := r.Driver.Delete(ctx, res, engine.ObjectStore{}); err != nil {
		return err
	}
	if err := r.syncEdge(ctx); err != nil { // the CDN is deleting, so its route goes
		return err
	}
	raw, _, err := api.NormalizeSpec(api.KindCDN, res.Spec)
	if err != nil {
		return nil
	}
	origin := raw.(api.CDNSpec).Origin
	kind, name, _ := api.ParseOrigin(origin)
	if kind != api.KindBucket {
		return nil
	}
	others, err := r.Store.ProjectResourcesOfKind(ctx, res.ProjectName(), api.KindCDN) // a bucket is its project's (ADR-035)
	if err != nil {
		return err
	}
	for _, o := range others {
		var s api.CDNSpec
		if o.ID != res.ID && o.Phase != api.PhaseDeleting && json.Unmarshal(o.Spec, &s) == nil && s.Origin == origin {
			return nil
		}
	}
	if _, err := r.Store.GetResourceByName(ctx, res.Project, api.KindBucket, name); errors.Is(err, store.ErrNotFound) {
		return nil
	}
	os, err := r.objectStore(ctx)
	if err != nil {
		return err
	}
	return r.Driver.SetBucketPublicRead(ctx, name, false, os)
}

// refreshCDN keeps a ready CDN's cached size and certificate current (CDN-07).
func (r *Reconciler) refreshCDN(ctx context.Context, res *domain.Resource) {
	if n, err := r.Driver.CacheUsage(ctx, res); err == nil && asIntOr(res.Outputs[engine.OutputCachedBytes]) != n {
		res.Outputs = withOutput(res.Outputs, engine.OutputCachedBytes, n)
		_, _ = r.Store.SaveStatus(ctx, res, res.Generation)
	}
	r.refreshCertificate(ctx, res)
}

// relay resolves an email relay's credentials secret, {"username", "password"} (EML-02).
func (r *Reconciler) relay(ctx context.Context, project string, s api.EmailRelay) (*engine.Relay, error) {
	raw, err := r.secretValue(ctx, project, s.CredentialsSecret)
	if err != nil {
		return nil, err
	}
	var c struct{ Username, Password string }
	if err := json.Unmarshal([]byte(raw), &c); err != nil || c.Username == "" || c.Password == "" {
		return nil, engine.Permanent(fmt.Errorf(`the secret %s must hold JSON {"username": "…", "password": "…"}`, s.CredentialsSecret))
	}
	return &engine.Relay{Host: s.Host, Port: s.Port, TLS: s.TLS, Username: c.Username, Password: c.Password}, nil
}
