// Package providers maps provider names to their plug-ins (ADR-023, L2-006 DNS-08): adding a provider
// is a new package and one line here, with no API change.
package providers

import (
	"fmt"

	"github.com/enclavestack/platform/internal/dns"
	"github.com/enclavestack/platform/internal/dns/cloudflare"
	"github.com/enclavestack/platform/internal/dns/route53"
)

// Open is the production dns.Opener.
func Open(provider, zone string, credentials []byte) (dns.Provider, error) {
	switch provider {
	case "cloudflare":
		return cloudflare.New(zone, credentials)
	case "route53":
		return route53.New(zone, credentials)
	}
	return nil, fmt.Errorf("%w: there's no DNS provider called %q", dns.ErrInvalid, provider)
}
