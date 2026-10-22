// Package lb renders load balancer configurations for HAProxy and reads its statistics (ADR-022). It
// is pure: no I/O, and it imports nothing of the control plane but the wire contract. Configuration text is built only from
// validated names and numbers (L3-006 §6).
package lb

import (
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/enclavestack/platform/pkg/api"
)

// Image is HAProxy 3.2 (Alpine), pinned by its index digest (DAR-014).
const Image = "haproxy@sha256:254e6370f01cd654e0744edf607c67f33777c1b6a9dc0d4b28bcba7171460122"

// ConfigPath is where the image reads its configuration.
const ConfigPath = "/usr/local/etc/haproxy/haproxy.cfg"

// StatsPort is the statistics listener, bound to the container's loopback only and read by exec. The
// API refuses it as a load balancer's port.
const StatsPort = api.LBStatsPort

// MaxReplicas is the number of server slots per target: the replica maximum (L2-006 LB-01).
const MaxReplicas = 10

// Target is one app behind the load balancer: its stable alias, port and weight.
type Target struct {
	Host   string
	Port   int
	Weight int
}

// Check is how HAProxy decides which replicas get traffic.
type Check struct {
	HTTP               bool
	Path               string
	IntervalSeconds    int
	TimeoutSeconds     int
	HealthyThreshold   int
	UnhealthyThreshold int
}

// Config is one load balancer.
type Config struct {
	HTTP      bool // http mode; otherwise tcp
	Port      int
	Algorithm string // round_robin, least_conn or source
	Check     Check
	Targets   []Target
}

var balance = map[string]string{"round_robin": "roundrobin", "least_conn": "leastconn", "source": "source"}

// Render returns HAProxy's configuration. Replicas are found through Docker's embedded DNS every second,
// so deploys add and remove them without a reload (LB-06).
func Render(c Config) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	mode := "tcp"
	if c.HTTP {
		mode = "http"
	}
	w("global")
	w("  master-worker")
	w("  log stdout format raw local0 notice")
	w("")
	w("resolvers docker")
	w("  nameserver dns 127.0.0.11:53")
	w("  resolve_retries 3")
	w("  timeout resolve 1s")
	w("  timeout retry 1s")
	w("  hold valid 1s")
	w("  hold obsolete 1s")
	w("  accepted_payload_size 8192")
	w("")
	w("defaults")
	w("  mode %s", mode)
	w("  log global")
	// Replicas are on the same machine: a connection that isn't up within 1 s goes to a stopped
	// container, whose address no longer answers. Fail fast so the retry below goes elsewhere.
	w("  timeout connect 1s")
	w("  timeout client 1m")
	w("  timeout server 1m")
	w("  timeout check %ds", max(c.Check.TimeoutSeconds, 1))
	// A replica that just stopped refuses connections until DNS and its checks catch up: retry the
	// connection on another replica. Only connection failures are retried, so nothing the app received
	// is ever sent twice (LB-06).
	w("  retries 3")
	w("  option redispatch 1")
	if c.HTTP {
		w("  option forwardfor")
		w("  retry-on conn-failure")
	}
	w("")
	w("frontend fe")
	w("  bind :%d", c.Port)
	w("  default_backend be")
	w("")
	w("backend be")
	alg := balance[c.Algorithm]
	if alg == "" {
		alg = "roundrobin"
	}
	w("  balance %s", alg)
	if c.Check.HTTP {
		w("  option httpchk GET %s", c.Check.Path)
		w("  http-check expect status 200-399")
	}
	for i, t := range c.Targets {
		w("  server-template t%d-r %d %s:%d check inter %ds rise %d fall %d weight %d resolvers docker init-addr none",
			i+1, MaxReplicas, t.Host, t.Port, max(c.Check.IntervalSeconds, 1), max(c.Check.HealthyThreshold, 1), max(c.Check.UnhealthyThreshold, 1), t.Weight)
	}
	w("")
	w("frontend stats")
	w("  mode http")
	w("  bind 127.0.0.1:%d", StatsPort)
	w("  stats enable")
	w("  stats uri /stats")
	return b.String()
}

// Status is how many replicas the load balancer found and how many pass their checks.
type Status struct {
	Targets int
	Healthy int
}

// ParseStats reads HAProxy's CSV statistics (`/stats;csv`). Slots that never resolved (MAINT) are
// not targets.
func ParseStats(r io.Reader) (Status, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	var st Status
	status := -1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			return st, nil
		}
		if err != nil {
			return st, err
		}
		if len(rec) > 0 && strings.HasPrefix(rec[0], "# ") {
			rec[0] = strings.TrimPrefix(rec[0], "# ")
			for i, h := range rec {
				if h == "status" {
					status = i
				}
			}
			continue
		}
		if status < 0 || len(rec) <= status || rec[0] != "be" || rec[1] == "BACKEND" || rec[1] == "FRONTEND" {
			continue
		}
		s := rec[status]
		if strings.HasPrefix(s, "MAINT") {
			continue
		}
		st.Targets++
		if strings.HasPrefix(s, "UP") {
			st.Healthy++
		}
	}
}
