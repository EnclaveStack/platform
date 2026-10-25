package docker

import "testing"

// Lines as Caddy 2.10 printed them on Docker Desktop (2026-10-22) with metrics.per_host on.
const caddyMetrics = `caddy_http_request_duration_seconds_sum{code="200",handler="static_response",host="a.localhost",method="GET",server="http"} 2.3417e-05
caddy_http_request_duration_seconds_count{code="200",handler="static_response",host="a.localhost",method="GET",server="http"} 1
caddy_http_request_duration_seconds_sum{code="503",handler="static_response",host="b.localhost",method="GET",server="http"} 7.458e-06
caddy_http_request_duration_seconds_count{code="503",handler="static_response",host="b.localhost",method="GET",server="http"} 1
caddy_http_request_duration_seconds_count{code="200",handler="reverse_proxy",host="b.localhost",method="GET",server="https"} 4`

func TestParseEdgeMetricsSumsByHost(t *testing.T) {
	got := parseEdgeMetrics(caddyMetrics)
	if a := got["a.localhost"]; a.Requests != 1 || a.Errors5xx != 0 || a.DurationSeconds != 2.3417e-05 {
		t.Errorf("a: %+v", a)
	}
	if b := got["b.localhost"]; b.Requests != 5 || b.Errors5xx != 1 {
		t.Errorf("b: %+v", b)
	}
	if len(parseEdgeMetrics("garbage\n# HELP x\n")) != 0 {
		t.Error("garbage parsed")
	}
}
