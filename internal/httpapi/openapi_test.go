package httpapi_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/enclavestack/platform/pkg/api"
)

func repoFile(parts ...string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(append([]string{filepath.Dir(file), "..", ".."}, parts...)...)
}

// TestOpenAPIMatchesTheServerRoutes keeps api/openapi.yaml and the router in step (L3-002).
func TestOpenAPIMatchesTheServerRoutes(t *testing.T) {
	raw, err := os.ReadFile(repoFile("api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	var spec []string
	for path, ops := range doc.Paths {
		for method := range ops {
			if method == "parameters" {
				continue
			}
			spec = append(spec, strings.ToUpper(method)+" "+path)
		}
	}
	src, err := os.ReadFile(repoFile("internal", "httpapi", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, m := range regexp.MustCompile(`s\.route\(mux, "([A-Z]+ /[^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		routes = append(routes, m[1])
	}
	routes = append(routes, "GET /metrics") // registered with mux.Handle
	slices.Sort(spec)
	slices.Sort(routes)
	if !slices.Equal(spec, routes) {
		t.Errorf("openapi.yaml and server routes differ\nspec:   %v\nserver: %v", spec, routes)
	}
}

// TestOpenAPIErrorCodesMatchPkgAPI: the documented error codes are exactly pkg/api's (L0-003 rule 1).
func TestOpenAPIErrorCodesMatchPkgAPI(t *testing.T) {
	raw, _ := os.ReadFile(repoFile("api", "openapi.yaml"))
	m := regexp.MustCompile(`(?s)code:\s*type: string\s*enum: \[([^\]]+)\]`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("error code enum not found in openapi.yaml")
	}
	var documented []string
	for _, c := range strings.Split(string(m[1]), ",") {
		documented = append(documented, strings.TrimSpace(c))
	}
	var defined []string
	for c := range api.ErrorCodes {
		defined = append(defined, string(c))
	}
	slices.Sort(documented)
	slices.Sort(defined)
	if !slices.Equal(documented, defined) {
		t.Errorf("error codes differ\nopenapi: %v\npkg/api: %v", documented, defined)
	}
}
