package api

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Preview environments (L2-016 PRV-01 – PRV-09, L3-002 §18.2, ADR-056).

// Preview limits and defaults.
const (
	PreviewTTLMin, PreviewTTLMax, PreviewTTLDefault          = 1, 720, 72
	PreviewMaxActiveMin, PreviewMaxActiveMax, PreviewMaxDefault = 1, 20, 5
	// HookBodyMax bounds a Git webhook's body.
	HookBodyMax = 1 << 20
	// HookPath is where Git webhooks arrive on the API; PublicHookPrefix is the same on public addresses.
	HookPath         = "/api/v1/hooks/git/"
	PublicHookPrefix = GatePathPrefix + "hooks/"
	PublicHookPath   = PublicHookPrefix + "git/"
	// The environment a preview adds (PRV-02).
	EnvPreview    = "ENCLAVE_PREVIEW"
	EnvPreviewRef = "ENCLAVE_PREVIEW_REF"
)

// Webhook results (L3-002 §18.2).
const (
	HookCreated    = "created"
	HookRedeployed = "redeployed"
	HookDeleted    = "deleted"
	HookDeployed   = "deployed"
	HookIgnored    = "ignored"
	HookPong       = "pong"
)

// PreviewDomain gives each preview "<preview>.<suffix>" (PRV-08).
type PreviewDomain struct {
	Suffix string `json:"suffix"`
	TLS    string `json:"tls,omitempty"`
	Zone   string `json:"zone,omitempty"`
	Tunnel string `json:"tunnel,omitempty"`
}

// Previews are an app's settings for its previews (PRV-01).
type Previews struct {
	Enabled   bool           `json:"enabled"`
	TTLHours  int            `json:"ttlHours,omitempty"`
	MaxActive int            `json:"maxActive,omitempty"`
	Domain    *PreviewDomain `json:"domain,omitempty"`
}

// On reports whether previews are turned on.
func (p *Previews) On() bool { return p != nil && p.Enabled }

// CreatePreview is the body of POST /resources/{app}/previews (PRV-06).
type CreatePreview struct {
	Ref  string `json:"ref"`
	Name string `json:"name,omitempty"`
}

// Preview is one of an app's previews.
type Preview struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Ref         string    `json:"ref"`
	PullRequest int       `json:"pullRequest,omitempty"`
	Phase       Phase     `json:"phase"`
	URL         string    `json:"url,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt"`
	CreatedAt   time.Time `json:"createdAt"`
}

// PreviewList is the body of GET /resources/{app}/previews.
type PreviewList struct {
	Items []Preview `json:"items"`
}

// HookResult answers a Git webhook.
type HookResult struct {
	Result  string `json:"result"`
	Preview string `json:"preview,omitempty"`
}

func normalizePreviews(p *Previews) {
	if p == nil {
		return
	}
	if p.TTLHours == 0 {
		p.TTLHours = PreviewTTLDefault
	}
	if p.MaxActive == 0 {
		p.MaxActive = PreviewMaxDefault
	}
	if d := p.Domain; d != nil {
		d.Suffix = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d.Suffix)), ".")
		if d.TLS == "" {
			d.TLS = TLSAuto
			if d.Zone == "" && d.Tunnel == "" {
				d.TLS = TLSInternal
			}
		}
	}
}

// validatePreviews checks an app's previews and webhook settings (PRV-01, PRV-03).
func validatePreviews(s AppSpec) []FieldError {
	var errs []FieldError
	if s.Source == nil {
		if s.Previews != nil {
			errs = append(errs, FieldError{Field: "spec.previews", Code: FieldNotAllowed, Message: "Previews build pull requests, so they need an app built from Git."})
		}
		return errs
	}
	if g := s.Source; g.WebhookSecret != "" && ValidateName(g.WebhookSecret) != nil {
		errs = append(errs, FieldError{Field: "spec.source.webhookSecret", Code: FieldInvalidFormat, Message: "Name an existing secret."})
	}
	p := s.Previews
	if p == nil {
		return errs
	}
	if p.TTLHours < PreviewTTLMin || p.TTLHours > PreviewTTLMax {
		errs = append(errs, FieldError{Field: "spec.previews.ttlHours", Code: FieldOutOfRange, Message: "Use 1–720 hours (30 days)."})
	}
	if p.MaxActive < PreviewMaxActiveMin || p.MaxActive > PreviewMaxActiveMax {
		errs = append(errs, FieldError{Field: "spec.previews.maxActive", Code: FieldOutOfRange, Message: "Use 1–20 previews at once."})
	}
	if d := p.Domain; d != nil {
		if e := ValidateHostname(d.Suffix); e != nil {
			errs = append(errs, FieldError{Field: "spec.previews.domain.suffix", Code: FieldInvalidFormat, Message: "Use a domain like preview.example.com; each preview is served at <preview>.preview.example.com."})
		}
		switch d.TLS {
		case TLSAuto, TLSInternal, TLSOff:
		default:
			errs = append(errs, FieldError{Field: "spec.previews.domain.tls", Code: FieldNotAllowed, Message: "Choose auto, internal or off."})
		}
		for field, name := range map[string]string{"spec.previews.domain.zone": d.Zone, "spec.previews.domain.tunnel": d.Tunnel} {
			if name != "" && ValidateName(name) != nil {
				errs = append(errs, FieldError{Field: field, Code: FieldInvalidFormat, Message: "Name an existing one."})
			}
		}
	}
	return errs
}

var refSlugJunk = regexp.MustCompile(`[^a-z0-9]+`)

// PreviewNameForPR is the name of an app's preview of pull request n, the app's name cut so it fits.
func PreviewNameForPR(app string, n int) string {
	return fitName(app, "-pr-"+strconv.Itoa(n))
}

// PreviewNameForRef is the default name of a preview made by hand from a ref (PRV-06).
func PreviewNameForRef(app, ref string) string {
	ref = strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/")
	slug := strings.Trim(refSlugJunk.ReplaceAllString(strings.ToLower(ref), "-"), "-")
	if slug == "" {
		slug = "preview"
	}
	if len(slug) > 20 {
		slug = strings.Trim(slug[:20], "-")
	}
	return fitName(app, "-"+slug)
}

func fitName(app, suffix string) string {
	if room := NameMaxLen - len(suffix); len(app) > room {
		app = strings.TrimRight(app[:room], "-")
	}
	return app + suffix
}

// PullRequestRef is the ref of a pull request's head on GitHub ("github") or GitLab ("gitlab").
func PullRequestRef(host string, n int) string {
	if host == "gitlab" {
		return fmt.Sprintf("refs/merge-requests/%d/head", n)
	}
	return fmt.Sprintf("refs/pull/%d/head", n)
}
