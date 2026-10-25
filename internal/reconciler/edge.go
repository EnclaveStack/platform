package reconciler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/internal/store"
	"github.com/enclavestack/platform/pkg/api"
)

// Domains and the edge proxy (ADR-015, L2-005 DOM-01 – DOM-09).

const (
	// DefaultCertWait bounds one wait for a certificate; a domain still waiting retries with backoff.
	DefaultCertWait  = 90 * time.Second
	certPollInterval = time.Second
	// certOverdueFraction: Caddy renews a certificate once a third of its lifetime is left. With less
	// than a tenth left, renewal has clearly failed and the domain goes back to provisioning. Relative,
	// because internal certificates live 12 hours and ACME ones 90 days.
	certOverdueFraction = 10
)

// edgeConfig renders every live domain into one edge configuration. Upstreams are the app's stable
// alias, so app deploys never change it (ADR-016).
func (r *Reconciler) edgeConfig(ctx context.Context) (engine.EdgeConfig, error) {
	var cfg engine.EdgeConfig
	domains, err := r.Store.ResourcesOfKind(ctx, api.KindDomain)
	if err != nil {
		return cfg, err
	}
	dns01 := map[string]bool{}
	for _, d := range domains {
		if d.Phase == api.PhaseDeleting {
			continue
		}
		raw, _, err := api.NormalizeSpec(api.KindDomain, d.Spec)
		if err != nil {
			continue
		}
		spec := raw.(api.DomainSpec)
		if spec.IsPublic() {
			continue // served only on its tunnel's listener, never on the machine's own ports (PUB-01)
		}
		app, err := r.Store.GetResourceByName(ctx, d.Project, api.KindApp, spec.App)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return cfg, err
		}
		port := spec.Port
		if port == 0 {
			if a, _, err := api.NormalizeSpec(api.KindApp, app.Spec); err == nil {
				port = a.(api.AppSpec).Port
			}
		}
		if port == 0 {
			continue
		}
		cfg.Routes = append(cfg.Routes, engine.Route{Hostname: spec.Hostname, Upstream: net.JoinHostPort(engine.ContainerName(app), strconv.Itoa(port)),
			TLS: spec.TLS, RedirectHTTP: spec.RedirectHTTP == nil || *spec.RedirectHTTP, DNS01: spec.Zone != ""})
		if spec.Zone != "" {
			dns01[spec.Hostname] = true
		}
	}
	// CDNs are hostnames too: the edge sends them to their cache container (ADR-033).
	cdns, err := r.Store.ResourcesOfKind(ctx, api.KindCDN)
	if err != nil {
		return cfg, err
	}
	for _, c := range cdns {
		if c.Phase == api.PhaseDeleting {
			continue
		}
		raw, _, err := api.NormalizeSpec(api.KindCDN, c.Spec)
		if err != nil {
			continue
		}
		spec := raw.(api.CDNSpec)
		cfg.Routes = append(cfg.Routes, engine.Route{Hostname: spec.Hostname, Upstream: net.JoinHostPort(engine.ContainerName(c), strconv.Itoa(engine.CDNPort)),
			TLS: spec.TLS, RedirectHTTP: spec.RedirectHTTP == nil || *spec.RedirectHTTP, DNS01: spec.Zone != ""})
		if spec.Zone != "" {
			dns01[spec.Hostname] = true
		}
	}
	listeners, relayDNS01, err := r.tunnelListeners(ctx)
	if err != nil {
		return cfg, err
	}
	cfg.Listeners, cfg.Gate = listeners, r.Gate
	for h := range relayDNS01 {
		dns01[h] = true
	}
	if cfg.Certificates, err = r.loadedCertificates(ctx, dns01); err != nil {
		return cfg, err
	}
	if email, err := r.Store.GetSetting(ctx, store.SettingACMEEmail); err == nil {
		cfg.ACMEEmail = email
	} else if errors.Is(err, store.ErrNotFound) {
		cfg.ACMEEmail, err = r.Store.FirstUserEmail(ctx)
		if err != nil {
			return cfg, err
		}
	} else {
		return cfg, err
	}
	return cfg, nil
}

// syncEdge applies the current configuration. Rendering and applying happen under one lock so a
// configuration computed earlier can never overwrite a newer one.
func (r *Reconciler) syncEdge(ctx context.Context) error {
	r.edgeMu.Lock()
	defer r.edgeMu.Unlock()
	cfg, err := r.edgeConfig(ctx)
	if err != nil {
		return err
	}
	if err := r.Driver.ConfigureEdge(ctx, cfg); err != nil {
		return err
	}
	r.edgeHash = edgeHash(cfg)
	return nil
}

// syncEdgeForApp re-applies the edge configuration when domains route to app (its port may have changed).
func (r *Reconciler) syncEdgeForApp(ctx context.Context, app *domain.Resource, log *slog.Logger) {
	deps, err := r.Store.Dependants(ctx, app.ID)
	if err != nil {
		log.Warn("list domains of app failed", "error", err)
		return
	}
	for _, d := range deps {
		if d.Kind == api.KindDomain || d.Kind == api.KindTunnel {
			if err := r.syncEdge(ctx); err != nil {
				log.Warn("update edge after app change failed", "error", err)
			}
			return
		}
	}
}

func isLoopback(addr string) bool {
	ip := net.ParseIP(addr)
	return addr == "localhost" || (ip != nil && ip.IsLoopback())
}

// ensureDomain routes the hostname and waits for its certificate.
func (r *Reconciler) ensureDomain(ctx context.Context, res *domain.Resource, report engine.Reporter) (engine.Outputs, error) {
	raw, _, err := api.NormalizeSpec(api.KindDomain, res.Spec)
	if err != nil {
		return nil, engine.Permanent(fmt.Errorf("stored spec unreadable: %w", err))
	}
	spec := raw.(api.DomainSpec)
	if spec.IsPublic() {
		return r.ensurePublicDomain(ctx, res, spec, report)
	}
	// A domain made local again takes back what it made in public first (PUB-06).
	if err := r.leavePublic(ctx, res); err != nil {
		return nil, err
	}
	// The edge may have just started: a firewall on the app must allow its address (ADR-021, FW-05).
	return r.serveHostname(ctx, res, spec, []string{"app/" + spec.App}, report)
}

// hostnameSpec is a domain's spec, or a CDN's hostname settings in a domain's form.
func hostnameSpec(res *domain.Resource) (api.DomainSpec, bool) {
	raw, _, err := api.NormalizeSpec(res.Kind, res.Spec)
	if err != nil {
		return api.DomainSpec{}, false
	}
	switch s := raw.(type) {
	case api.DomainSpec:
		return s, true
	case api.CDNSpec:
		return s.AsDomain(), true
	}
	return api.DomainSpec{}, false
}

// serveHostname routes a domain's or CDN's hostname through the edge and waits for its certificate
// (ADR-015, ADR-024, ADR-033); firewalls of targets are re-applied once the edge runs.
func (r *Reconciler) serveHostname(ctx context.Context, res *domain.Resource, spec api.DomainSpec, targets []string, report engine.Reporter) (engine.Outputs, error) {
	public, _ := r.Store.GetSetting(ctx, store.SettingPublicAddress)

	if spec.Zone != "" {
		// The certificate comes first, so the edge's first configuration already serves it (ADR-024).
		report.Step("certificate", api.StepActive)
		if err := r.obtainDNS01(ctx, res, spec, report); err != nil {
			return nil, err
		}
	}
	report.Step("edge", api.StepActive)
	if err := r.syncEdge(ctx); err != nil {
		return nil, err
	}
	if _, _, err := r.syncTargets(ctx, res.ProjectName(), targets); err != nil {
		return nil, err
	}
	st := r.Driver.EdgeStatus(ctx)
	report.Step("edge", api.StepDone)
	report.Step("route", api.StepActive)
	report.Step("route", api.StepDone)

	out := engine.Outputs{"url": domainURL(spec, st), "certificateState": engine.CertNone}
	if public != "" {
		out["publicAddress"] = public
	}
	report.Step("certificate", api.StepActive)
	if spec.TLS == api.TLSOff {
		report.Step("certificate", api.StepDone)
		return out, nil
	}
	if spec.TLS == api.TLSAuto && spec.Zone == "" && isLoopback(st.Address) { // DNS-01 needs no inbound port
		return nil, engine.Permanent(fmt.Errorf("the edge proxy listens only on %s, so Let's Encrypt can't reach %s. Restart enclave server with --edge-address 0.0.0.0, forward ports %d and %d to this machine, then retry",
			st.Address, spec.Hostname, st.HTTPPort, st.HTTPSPort))
	}
	wait := r.CertWait
	if wait == 0 {
		wait = DefaultCertWait
	}
	deadline := r.Now().Add(wait)
	for {
		cert, err := r.Driver.Certificate(ctx, checkName(spec.Hostname))
		if err != nil {
			return nil, err
		}
		if cert.State == engine.CertIssued {
			out["certificateState"], out["certificateIssuer"] = engine.CertIssued, cert.Issuer
			out["certificateExpiresAt"] = cert.NotAfter.UTC().Format(time.RFC3339)
			report.Step("certificate", api.StepDone)
			return out, nil
		}
		if !r.Now().Before(deadline) {
			break
		}
		engine.Note(report, "Waiting for the certificate for "+spec.Hostname+".")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(certPollInterval):
		}
	}
	if spec.TLS == api.TLSInternal {
		return nil, fmt.Errorf("the internal certificate for %s isn't ready yet", spec.Hostname)
	}
	if spec.Zone != "" {
		return nil, fmt.Errorf("the edge doesn't serve the certificate for %s yet", spec.Hostname)
	}
	target := public
	if target == "" {
		target = "this machine's public IP"
	}
	return nil, fmt.Errorf("no certificate for %s yet. Point its A (or AAAA) record to %s and make sure ports %d and %d reach this machine",
		spec.Hostname, target, st.HTTPPort, st.HTTPSPort)
}

// renewalOverdue reports a certificate that is expired or has less than a tenth of its lifetime left.
func renewalOverdue(c engine.Certificate, now time.Time) bool {
	left := c.NotAfter.Sub(now)
	life := c.NotAfter.Sub(c.NotBefore)
	if life <= 0 {
		return left <= 0
	}
	return left < life/certOverdueFraction
}

func domainURL(s api.DomainSpec, st engine.EdgeStatus) string {
	if s.TLS == api.TLSOff {
		if st.HTTPPort != 0 && st.HTTPPort != 80 {
			return "http://" + s.Hostname + ":" + strconv.Itoa(st.HTTPPort)
		}
		return "http://" + s.Hostname
	}
	if st.HTTPSPort != 0 && st.HTTPSPort != 443 {
		return "https://" + s.Hostname + ":" + strconv.Itoa(st.HTTPSPort)
	}
	return "https://" + s.Hostname
}

// refreshCertificate keeps a ready domain's certificate facts current and sends it back to
// provisioning when the certificate is gone, expired or about to expire (L3-007 §8).
func (r *Reconciler) refreshCertificate(ctx context.Context, res *domain.Resource) {
	spec, ok := hostnameSpec(res)
	if !ok {
		return
	}
	if spec.TLS == api.TLSOff {
		return
	}
	if spec.IsPublic() && res.Outputs["certificateState"] == certificateEdge {
		return // Cloudflare serves it (PUB-02)
	}
	if spec.Zone != "" {
		r.refreshDNS01(ctx, res, spec)
		if res.Phase != api.PhaseReady {
			return
		}
	}
	cert, err := r.Driver.Certificate(ctx, checkName(spec.Hostname))
	if err != nil {
		return
	}
	now := r.Now()
	if cert.State != engine.CertIssued || renewalOverdue(cert, now) {
		why := "its certificate is no longer served"
		if cert.State == engine.CertIssued {
			why = "its certificate expires on " + cert.NotAfter.UTC().Format("2 Jan 2006")
		}
		if err := res.SetPhase(api.PhaseProvisioning, "Renewing: "+why+".", now); err != nil {
			return
		}
		res.Steps, res.UpdatedAt = domain.NewSteps(domain.StepNames[res.Kind]...), now
		if ok, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil && ok {
			r.event(ctx, res, "certificate_renewal", "Checking the certificate again: "+why+".")
			r.Enqueue(res.ID)
		}
		return
	}
	expires := cert.NotAfter.UTC().Format(time.RFC3339)
	if res.Outputs["certificateExpiresAt"] == expires && res.Outputs["certificateIssuer"] == cert.Issuer {
		return
	}
	res.Outputs = withOutput(withOutput(res.Outputs, "certificateExpiresAt", expires), "certificateIssuer", cert.Issuer)
	res.UpdatedAt = now
	if _, err := r.Store.SaveStatus(ctx, res, res.Generation); err == nil {
		r.event(ctx, res, "certificate_renewed", "The certificate now expires on "+cert.NotAfter.UTC().Format("2 Jan 2006")+".")
	}
}
