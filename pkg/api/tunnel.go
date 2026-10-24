package api

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// Public access (L2-014, L3-002 §16): tunnels, public domains and the gate.

// Tunnel providers. App links give one app an address the provider hands out; domain tunnels serve the
// public domains that name them (TUN-01).
const (
	TunnelQuick      = "quick"
	TunnelTailscale  = "tailscale"
	TunnelNgrok      = "ngrok"
	TunnelCustom     = "custom"
	TunnelCloudflare = "cloudflare"
	TunnelRelay      = "relay"
)

// Gate modes (GATE-01) and Tailscale modes (TUN-12).
const (
	GateSignin   = "signin"
	GatePassword = "password"
	GateNone     = "none"

	TailscalePrivate = "private"
	TailscalePublic  = "public"
)

// Limits and reserved names (L3-002 §16.5).
const (
	GateOpenPathsMax     = 10
	GatePathMaxLen       = 255
	AccessEmailsMax      = 50
	AccessEmailDomainMax = 10
	// GatePathPrefix is the path EnclaveStack reserves on every gated host for the gate's own pages.
	GatePathPrefix = "/.enclave/"
	// UpstreamEnv is where a custom connector must send traffic (TUN-14).
	UpstreamEnv = "ENCLAVE_UPSTREAM"
	// UserHeader carries a signed-in visitor's email to the app (GATE-07).
	UserHeader = "X-Enclave-User"
)

var (
	TunnelProviders       = []string{TunnelQuick, TunnelTailscale, TunnelNgrok, TunnelCustom, TunnelCloudflare, TunnelRelay}
	AppLinkProviders      = []string{TunnelQuick, TunnelTailscale, TunnelNgrok, TunnelCustom}
	DomainTunnelProviders = []string{TunnelCloudflare, TunnelRelay}
	GateModes             = []string{GateSignin, GatePassword, GateNone}
	TailscaleModes        = []string{TailscalePrivate, TailscalePublic}
	// CredentialedProviders need a credentials secret: the token itself, or JSON with this one key.
	CredentialedProviders = map[string]string{TunnelCloudflare: "apiToken", TunnelTailscale: "authKey", TunnelNgrok: "authToken"}
)

// IsAppLink reports whether a provider gives one app its own address.
func IsAppLink(provider string) bool { return slices.Contains(AppLinkProviders, provider) }

// Gate is what stands between the internet and a public app (GATE-01 – GATE-06).
type Gate struct {
	Mode           string   `json:"mode"`
	PasswordSecret string   `json:"passwordSecret,omitempty"`
	OpenPaths      []string `json:"openPaths,omitempty"`
}

// CloudflareAccess lists who Cloudflare lets through before the gate (GATE-09).
type CloudflareAccess struct {
	Emails       []string `json:"emails,omitempty"`
	EmailDomains []string `json:"emailDomains,omitempty"`
}

// TunnelSpec is a way in from the internet (TUN-01 – TUN-16).
type TunnelSpec struct {
	Provider string `json:"provider"`
	// CredentialsSecret holds the provider's token: Cloudflare's API token, a Tailscale auth key or an ngrok
	// authtoken, as the value itself or as JSON {"apiToken"} / {"authKey"} / {"authToken"}.
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// AccountID is the Cloudflare account the tunnel is made in.
	AccountID string `json:"accountId,omitempty"`
	// Machine is the edge machine a relay runs on.
	Machine string `json:"machine,omitempty"`
	// App and Port are the app an app link reaches; Port defaults to the app's.
	App  string `json:"app,omitempty"`
	Port int    `json:"port,omitempty"`
	// Gate protects an app link (a domain carries its own).
	Gate *Gate `json:"gate,omitempty"`
	// Mode and Hostname are Tailscale's: private (Serve) or public (Funnel), and the device's name.
	Mode     string `json:"mode,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// Domain is an ngrok domain reserved in the account.
	Domain string `json:"domain,omitempty"`
	// Image, Command, Env, SecretEnv and URL describe a custom connector.
	Image     string            `json:"image,omitempty"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	SecretEnv map[string]string `json:"secretEnv,omitempty"`
	URL       string            `json:"url,omitempty"`
}

// GateOrDefault is the gate in force: as given, or the default (signin; none for a private Tailscale link).
func (s TunnelSpec) GateOrDefault() Gate {
	if s.Gate != nil {
		return *s.Gate
	}
	if s.Provider == TunnelTailscale && s.Mode == TailscalePrivate {
		return Gate{Mode: GateNone}
	}
	return Gate{Mode: GateSignin}
}

// IsPublic reports whether a domain is served through a tunnel (PUB-01).
func (s DomainSpec) IsPublic() bool { return s.Tunnel != "" }

// GateOrDefault is a public domain's gate: as given, or signin (GATE-01).
func (s DomainSpec) GateOrDefault() Gate {
	if s.Gate != nil {
		return *s.Gate
	}
	return Gate{Mode: GateSignin}
}

var (
	tailscaleHostPattern = regexp.MustCompile(HostLabelPatternSource)
	accountIDPattern     = regexp.MustCompile(`^[a-f0-9]{32}$`)
)

func normalizeTunnel(s TunnelSpec) TunnelSpec {
	s.Provider = strings.TrimSpace(s.Provider)
	if s.Provider == TunnelTailscale && s.Mode == "" {
		s.Mode = TailscalePrivate
	}
	s.Hostname = strings.ToLower(strings.TrimSpace(s.Hostname))
	s.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Domain)), ".")
	s.URL = strings.TrimSpace(s.URL)
	s.AccountID = strings.ToLower(strings.TrimSpace(s.AccountID))
	if s.Gate != nil {
		g := normalizeGate(*s.Gate)
		s.Gate = &g
	}
	return s
}

func normalizeGate(g Gate) Gate {
	g.Mode = strings.TrimSpace(g.Mode)
	for i := range g.OpenPaths {
		g.OpenPaths[i] = strings.TrimSpace(g.OpenPaths[i])
	}
	return g
}

func validateTunnel(s TunnelSpec) []FieldError {
	var errs []FieldError
	if !slices.Contains(TunnelProviders, s.Provider) {
		return []FieldError{{Field: "spec.provider", Code: FieldNotAllowed, Message: "Choose quick, tailscale, ngrok, custom, cloudflare or relay."}}
	}
	notAllowed := func(field, msg string) {
		errs = append(errs, FieldError{Field: field, Code: FieldNotAllowed, Message: msg})
	}
	_, needsCreds := CredentialedProviders[s.Provider]
	switch {
	case needsCreds && s.CredentialsSecret == "":
		errs = append(errs, FieldError{Field: "spec.credentialsSecret", Code: FieldRequired,
			Message: map[string]string{TunnelCloudflare: "Name the secret holding the Cloudflare API token.", TunnelTailscale: "Name the secret holding the Tailscale auth key.",
				TunnelNgrok: "Name the secret holding the ngrok authtoken."}[s.Provider]})
	case needsCreds && ValidateName(s.CredentialsSecret) != nil:
		errs = append(errs, FieldError{Field: "spec.credentialsSecret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
	case !needsCreds && s.CredentialsSecret != "":
		notAllowed("spec.credentialsSecret", "This way in needs no credentials.")
	}
	if s.Provider == TunnelCloudflare {
		if !accountIDPattern.MatchString(s.AccountID) {
			errs = append(errs, FieldError{Field: "spec.accountId", Code: FieldInvalidFormat, Message: "Give the Cloudflare account ID: 32 letters and digits, on the domain's overview page."})
		}
	} else if s.AccountID != "" {
		notAllowed("spec.accountId", "Only a Cloudflare tunnel names an account.")
	}
	if s.Provider == TunnelRelay {
		if s.Machine == "" {
			errs = append(errs, FieldError{Field: "spec.machine", Code: FieldRequired, Message: "Choose the edge machine the relay runs on."})
		} else if ValidateName(s.Machine) != nil {
			errs = append(errs, FieldError{Field: "spec.machine", Code: FieldInvalidFormat, Message: "Name an edge machine."})
		}
	} else if s.Machine != "" {
		notAllowed("spec.machine", "Only a relay runs on an edge machine.")
	}
	if IsAppLink(s.Provider) {
		switch {
		case s.App == "":
			errs = append(errs, FieldError{Field: "spec.app", Code: FieldRequired, Message: "Choose the app this link makes public."})
		case ValidateName(s.App) != nil:
			errs = append(errs, FieldError{Field: "spec.app", Code: FieldInvalidFormat, Message: "Name an existing app."})
		}
		if s.Port != 0 && (s.Port < PortMin || s.Port > PortMax) {
			errs = append(errs, FieldError{Field: "spec.port", Code: FieldOutOfRange, Message: "Use a port from 1 to 65535."})
		}
		if s.Gate != nil {
			errs = append(errs, validateGate(*s.Gate, "spec.gate")...)
		}
	} else {
		for field, set := range map[string]bool{"spec.app": s.App != "", "spec.port": s.Port != 0, "spec.gate": s.Gate != nil} {
			if set {
				notAllowed(field, "A domain tunnel serves the domains that name it; set the app and gate on each domain.")
			}
		}
	}
	if s.Provider == TunnelTailscale {
		if !slices.Contains(TailscaleModes, s.Mode) {
			notAllowed("spec.mode", "Choose private or public.")
		}
		if s.Hostname != "" && !tailscaleHostPattern.MatchString(s.Hostname) {
			errs = append(errs, FieldError{Field: "spec.hostname", Code: FieldInvalidFormat, Message: "Use lowercase letters, digits and hyphens, at most 63, like galiaraa-stage."})
		}
	} else {
		if s.Mode != "" {
			notAllowed("spec.mode", "Only a Tailscale link is private or public.")
		}
		if s.Hostname != "" {
			notAllowed("spec.hostname", "Only a Tailscale link chooses its device name.")
		}
	}
	if s.Domain != "" {
		if s.Provider != TunnelNgrok {
			notAllowed("spec.domain", "Only an ngrok link names a reserved domain.")
		} else if e := ValidateHostname(s.Domain); e != nil {
			errs = append(errs, FieldError{Field: "spec.domain", Code: FieldInvalidFormat, Message: "Use a domain reserved in your ngrok account, like stage.ngrok.app."})
		}
	}
	if s.Provider == TunnelCustom {
		errs = append(errs, validateImage(s.Image, "spec.image")...)
		errs = append(errs, validateCommand(s.Command)...)
		errs = append(errs, validateEnv(s.Env, s.SecretEnv)...)
		for field, m := range map[string]map[string]string{"spec.env.": s.Env, "spec.secretEnv.": s.SecretEnv} {
			if _, ok := m[UpstreamEnv]; ok {
				errs = append(errs, FieldError{Field: field + UpstreamEnv, Code: FieldNotAllowed, Message: "EnclaveStack sets ENCLAVE_UPSTREAM itself."})
			}
		}
		if s.URL != "" {
			if u, err := url.Parse(s.URL); err != nil || u.Scheme != "https" || u.Host == "" {
				errs = append(errs, FieldError{Field: "spec.url", Code: FieldInvalidFormat, Message: "Use the https:// address the service gives."})
			}
		}
	} else {
		for field, set := range map[string]bool{"spec.image": s.Image != "", "spec.command": len(s.Command) > 0, "spec.env": len(s.Env) > 0,
			"spec.secretEnv": len(s.SecretEnv) > 0, "spec.url": s.URL != ""} {
			if set {
				notAllowed(field, "Only a custom connector sets its image, command, environment and address.")
			}
		}
	}
	slices.SortStableFunc(errs, func(a, b FieldError) int { return strings.Compare(a.Field, b.Field) })
	return errs
}

func validateGate(g Gate, field string) []FieldError {
	var errs []FieldError
	if !slices.Contains(GateModes, g.Mode) {
		errs = append(errs, FieldError{Field: field + ".mode", Code: FieldNotAllowed, Message: "Choose signin, password or none."})
	}
	switch {
	case g.Mode == GatePassword && g.PasswordSecret == "":
		errs = append(errs, FieldError{Field: field + ".passwordSecret", Code: FieldRequired, Message: `Name the secret holding {"username","password"}.`})
	case g.Mode == GatePassword && ValidateName(g.PasswordSecret) != nil:
		errs = append(errs, FieldError{Field: field + ".passwordSecret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
	case g.Mode != GatePassword && g.PasswordSecret != "":
		errs = append(errs, FieldError{Field: field + ".passwordSecret", Code: FieldNotAllowed, Message: "Only the password gate uses a secret."})
	}
	if len(g.OpenPaths) > GateOpenPathsMax {
		errs = append(errs, FieldError{Field: field + ".openPaths", Code: FieldTooLong, Message: "Use at most 10 open paths."})
	}
	for i, p := range g.OpenPaths {
		f := fmt.Sprintf("%s.openPaths[%d]", field, i)
		switch {
		case !strings.HasPrefix(p, "/") || len(p) > GatePathMaxLen || strings.Contains(p, "..") || strings.ContainsAny(p, "*?# \t\r\n"):
			errs = append(errs, FieldError{Field: f, Code: FieldInvalidFormat, Message: "Use a path that starts with /, like /webhooks/."})
		case strings.HasPrefix(p+"/", GatePathPrefix) || strings.HasPrefix(p, GatePathPrefix):
			errs = append(errs, FieldError{Field: f, Code: FieldNotAllowed, Message: "Paths under /.enclave/ belong to the gate."})
		}
	}
	return errs
}

func normalizeAccess(a *CloudflareAccess) *CloudflareAccess {
	if a == nil {
		return nil
	}
	out := CloudflareAccess{}
	for _, e := range a.Emails {
		out.Emails = append(out.Emails, strings.ToLower(strings.TrimSpace(e)))
	}
	for _, d := range a.EmailDomains {
		out.EmailDomains = append(out.EmailDomains, strings.TrimPrefix(strings.ToLower(strings.TrimSpace(d)), "@"))
	}
	return &out
}

func validateAccess(a CloudflareAccess) []FieldError {
	var errs []FieldError
	if len(a.Emails)+len(a.EmailDomains) == 0 {
		errs = append(errs, FieldError{Field: "spec.cloudflareAccess", Code: FieldRequired, Message: "List at least one email or email domain."})
	}
	if len(a.Emails) > AccessEmailsMax {
		errs = append(errs, FieldError{Field: "spec.cloudflareAccess.emails", Code: FieldTooLong, Message: "Use at most 50 emails."})
	}
	if len(a.EmailDomains) > AccessEmailDomainMax {
		errs = append(errs, FieldError{Field: "spec.cloudflareAccess.emailDomains", Code: FieldTooLong, Message: "Use at most 10 email domains."})
	}
	for i, e := range a.Emails {
		if ValidateEmail(e) != nil {
			errs = append(errs, FieldError{Field: fmt.Sprintf("spec.cloudflareAccess.emails[%d]", i), Code: FieldInvalidFormat, Message: "Enter an email address like you@example.com."})
		}
	}
	for i, d := range a.EmailDomains {
		if ValidateHostname(d) != nil {
			errs = append(errs, FieldError{Field: fmt.Sprintf("spec.cloudflareAccess.emailDomains[%d]", i), Code: FieldInvalidFormat, Message: "Enter a domain like galiaraa.com."})
		}
	}
	return errs
}

// validateDomainAccess checks a domain's public-access fields (L3-002 §16.5); the tunnel's provider is
// checked by the service, which can see the tunnel.
func validateDomainAccess(s DomainSpec) []FieldError {
	var errs []FieldError
	if s.Tunnel == "" {
		if s.Gate != nil {
			errs = append(errs, FieldError{Field: "spec.gate", Code: FieldNotAllowed, Message: "Only a public domain has a gate. Choose a tunnel first."})
		}
		if s.CloudflareAccess != nil {
			errs = append(errs, FieldError{Field: "spec.cloudflareAccess", Code: FieldNotAllowed, Message: "Cloudflare Access needs a Cloudflare tunnel."})
		}
		return errs
	}
	if ValidateName(s.Tunnel) != nil {
		errs = append(errs, FieldError{Field: "spec.tunnel", Code: FieldInvalidFormat, Message: "Name an existing tunnel."})
	}
	if s.TLS != TLSAuto {
		errs = append(errs, FieldError{Field: "spec.tls", Code: FieldNotAllowed, Message: "A public name needs a public certificate: use tls auto."})
	}
	if strings.HasSuffix(s.Hostname, LocalhostSuffix) {
		errs = append(errs, FieldError{Field: "spec.hostname", Code: FieldNotAllowed, Message: "A .localhost name can't be public. Use a domain you own."})
	}
	if s.Gate != nil {
		errs = append(errs, validateGate(*s.Gate, "spec.gate")...)
	}
	if s.CloudflareAccess != nil {
		errs = append(errs, validateAccess(*s.CloudflareAccess)...)
	}
	return errs
}
