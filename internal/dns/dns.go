// Package dns is EnclaveStack's DNS provider plug-in interface (ADR-023). Each provider keeps records of
// one zone at an external service; the reconciler decides ownership and conflicts on top of it. The
// package imports nothing of the control plane, like internal/build.
package dns

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Zone is what a provider reports about its zone.
type Zone struct {
	ID          string
	NameServers []string
}

// Record is one name and type at the provider, with all its values.
type Record struct {
	FQDN   string // without the trailing dot
	Type   string // A, AAAA, CNAME or TXT
	Values []string
	TTL    int
	// Owner is the EnclaveStack resource that wrote it, when the provider can store that (Cloudflare record
	// comments); "" when unknown.
	Owner string
	// Proxied sends the name's traffic through the provider's network (Cloudflare only): what a public
	// domain on a Cloudflare tunnel needs (L2-014 PUB-02). Other providers ignore it.
	Proxied bool
}

// Provider keeps records of one zone.
type Provider interface {
	// Zone checks the credentials can read the zone and reports it.
	Zone(ctx context.Context) (Zone, error)
	// Get returns the record of a name and type, or nil when there is none.
	Get(ctx context.Context, fqdn, typ string) (*Record, error)
	// Set makes the name and type hold exactly rec's values, marking them as owner's where it can.
	Set(ctx context.Context, rec Record, owner string) error
	// Delete removes the name and type; deleting a missing record is not an error.
	Delete(ctx context.Context, fqdn, typ string) error
}

// Opener builds a provider for a zone from its decrypted credentials (JSON).
type Opener func(provider, zone string, credentials []byte) (Provider, error)

// Error classes the reconciler maps to retry behaviour (L3-007 §9).
var (
	// ErrPermission: the credentials are rejected or lack a permission. Retrying can't help.
	ErrPermission = errors.New("the provider rejected the credentials")
	// ErrNoZone: the credentials work but the zone isn't there.
	ErrNoZone = errors.New("the zone doesn't exist at the provider")
	// ErrInvalid: the provider refused the request itself (a value it doesn't accept).
	ErrInvalid = errors.New("the provider refused the request")
)

// RateLimitedError asks to retry after a delay (HTTP 429).
type RateLimitedError struct{ RetryAfter time.Duration }

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("the provider is rate limiting requests; retry in %s", e.RetryAfter.Round(time.Second))
}

// Permanent reports whether retrying err can't succeed.
func Permanent(err error) bool {
	return errors.Is(err, ErrPermission) || errors.Is(err, ErrNoZone) || errors.Is(err, ErrInvalid)
}

// CallTimeout bounds one provider call (L3-007 §9).
const CallTimeout = 30 * time.Second

// OwnerComment is how EnclaveStack marks records it wrote where a provider stores comments.
func OwnerComment(owner string) string { return "managed by enclave " + owner }

// SameValues compares value sets regardless of order, case (for names) and TXT quoting.
func SameValues(typ string, a, b []string) bool {
	na, nb := Normalize(typ, a), Normalize(typ, b)
	return slices.Equal(na, nb)
}

// Normalize returns a record's values in canonical, sorted form.
func Normalize(typ string, values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		v = strings.TrimSpace(v)
		switch typ {
		case "TXT":
			if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
				v = v[1 : len(v)-1]
			}
		case "CNAME":
			v = strings.TrimSuffix(strings.ToLower(v), ".")
		}
		out[i] = v
	}
	slices.Sort(out)
	return slices.Compact(out)
}
