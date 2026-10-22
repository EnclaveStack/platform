package lb

import (
	"strings"
	"testing"
)

func TestRenderHTTPWithWeightsAndChecks(t *testing.T) {
	got := Render(Config{HTTP: true, Port: 80, Algorithm: "least_conn",
		Check:   Check{HTTP: true, Path: "/healthz", IntervalSeconds: 2, TimeoutSeconds: 1, HealthyThreshold: 2, UnhealthyThreshold: 3},
		Targets: []Target{{Host: "es-app-api", Port: 8080, Weight: 3}, {Host: "es-app-canary", Port: 8080, Weight: 1}}})
	for _, want := range []string{
		"  mode http\n", "  option forwardfor\n", "  bind :80\n", "  balance leastconn\n",
		"  option httpchk GET /healthz\n", "  http-check expect status 200-399\n",
		"  server-template t1-r 10 es-app-api:8080 check inter 2s rise 2 fall 3 weight 3 resolvers docker init-addr none\n",
		"  server-template t2-r 10 es-app-canary:8080 check inter 2s rise 2 fall 3 weight 1 resolvers docker init-addr none\n",
		"  bind 127.0.0.1:61999\n", "  nameserver dns 127.0.0.11:53\n", "  master-worker\n",
		"  retries 3\n", "  option redispatch 1\n", "  retry-on conn-failure\n", "  timeout connect 1s\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderTCPDefaults(t *testing.T) {
	got := Render(Config{Port: 5432, Targets: []Target{{Host: "es-app-pg", Port: 5432, Weight: 1}}})
	if !strings.Contains(got, "  mode tcp\n") || strings.Contains(got, "forwardfor") || strings.Contains(got, "httpchk") || !strings.Contains(got, "balance roundrobin") {
		t.Errorf("tcp config:\n%s", got)
	}
	cfg := Config{Port: 1, Targets: []Target{{Host: "a", Port: 1, Weight: 1}, {Host: "b", Port: 2, Weight: 2}}}
	first, second := Render(cfg), Render(cfg)
	if first != second {
		t.Error("not deterministic")
	}
}

func TestParseStats(t *testing.T) {
	csv := `# pxname,svname,qcur,qmax,scur,smax,slim,stot,bin,bout,dreq,dresp,ereq,econ,eresp,wretr,wredis,status,weight
fe,FRONTEND,,,0,0,,0,0,0,0,0,0,,,,,OPEN,
be,t1-r1,0,0,0,0,,0,0,0,,0,,0,0,0,0,UP,1
be,t1-r2,0,0,0,0,,0,0,0,,0,,0,0,0,0,DOWN,1
be,t1-r3,0,0,0,0,,0,0,0,,0,,0,0,0,0,MAINT (resolution),1
be,t2-r1,0,0,0,0,,0,0,0,,0,,0,0,0,0,UP 1/2,1
be,BACKEND,0,0,0,0,,0,0,0,0,0,,0,0,0,0,UP,2
stats,FRONTEND,,,1,1,,1,0,0,0,0,0,,,,,OPEN,
`
	st, err := ParseStats(strings.NewReader(csv))
	if err != nil || st != (Status{Targets: 3, Healthy: 2}) {
		t.Errorf("status %+v %v", st, err)
	}
}
