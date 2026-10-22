// Package archtest enforces the dependency rules in L3-001 §2: arrows point inward, and only the
// reconciler changes runtime state through the engine.
package archtest

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const module = "github.com/enclavestack/platform/"

// forbidden maps a package directory to imports it must never have.
var forbidden = map[string][]string{
	"pkg/api":             {"internal/"},
	"pkg/client":          {"internal/"},
	"internal/domain":     {"internal/store", "internal/httpapi", "internal/engine", "internal/service", "internal/reconciler", "net/http", "database/sql"},
	"internal/store":      {"internal/httpapi", "internal/service", "internal/reconciler", "internal/engine"},
	"internal/service":    {"internal/httpapi", "internal/cli", "internal/reconciler", "internal/engine/docker", "internal/engine/fake"},
	"internal/httpapi":    {"internal/engine", "internal/reconciler", "internal/cli"},
	"internal/reconciler": {"internal/httpapi", "internal/cli", "internal/service"},
	"internal/engine":     {"internal/store", "internal/httpapi", "internal/service", "internal/reconciler"},
	"internal/provider":   {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi"},
	"internal/cli":        {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi"},
	"internal/build":      {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi", "internal/cli"},
	"internal/firewall":   {"internal/", "pkg/"}, // pure rendering (ADR-021)
	"internal/lb":         {"internal/"},         // pure rendering (ADR-022)
	"internal/dns":        {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi", "internal/cli", "internal/acme"},
	"internal/acme":       {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi", "internal/cli"},
	// v0.6: the broker port, the mail client and the FFmpeg presets know nothing of the control plane.
	"internal/messaging": {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi", "internal/cli", "internal/domain", "pkg/"},
	"internal/mail":      {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi", "internal/cli", "internal/domain"},
	"internal/transcode": {"internal/"},
	// v0.7: the monitor reads through the engine like a service; the store clients, webhooks and the
	// signer know nothing of the control plane (ADR-037 – ADR-041).
	"internal/monitor":      {"internal/httpapi", "internal/cli", "internal/service", "internal/reconciler", "internal/engine/docker", "internal/engine/fake"},
	"internal/secretstores": {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi", "internal/cli", "internal/domain"},
	"internal/webhook":      {"internal/", "pkg/"},
	"internal/sigv4":        {"internal/", "pkg/"},
	// v0.8: the agent has no store and serves only its machine's engine; the machines package is reached by
	// the router only through its port (ADR-042 – ADR-044).
	"internal/agent":    {"internal/store", "internal/service", "internal/reconciler", "internal/httpapi", "internal/cli", "internal/engine/cluster"},
	"internal/machines": {"internal/httpapi", "internal/cli", "internal/service", "internal/reconciler"},
	"internal/mesh":     {"internal/store", "internal/service", "internal/engine", "internal/reconciler", "internal/httpapi", "internal/cli", "internal/domain", "pkg/"},
}

func root(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func TestImportRules(t *testing.T) {
	base := root(t)
	for dir, bans := range forbidden {
		err := filepath.WalkDir(filepath.Join(base, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p := strings.Trim(imp.Path.Value, `"`)
				for _, ban := range bans {
					target := ban
					if strings.HasPrefix(ban, "internal/") || strings.HasPrefix(ban, "pkg/") {
						target = module + ban
					}
					if p == target || strings.HasPrefix(p, strings.TrimSuffix(target, "/")+"/") {
						rel, _ := filepath.Rel(base, path)
						t.Errorf("%s imports %s, which L3-001 §2 forbids for %s", rel, p, dir)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// TestOnlyTheReconcilerWritesThroughTheEngine: services and the monitor may read (Info, Logs, EdgeStatus, RootCA,
// BrokerStatus; the hypervisor's Check, ConsoleLog, CachedImages) but never call the state-changing
// driver, hypervisor or broker-control methods (ADR-025, ADR-029).
func TestOnlyTheReconcilerWritesThroughTheEngine(t *testing.T) {
	base := root(t)
	writes := []string{".Ensure(", ".Delete(ctx, r,", ".Restart(ctx, r", ".ConfigureEdge(", ".StartJob(", ".WaitJob(", ".Build(ctx", ".RemoveBuiltImages(",
		".EnsureNetwork(", ".DeleteNetwork(", ".ApplyFirewall(", ".EnsureVolume(", ".SnapshotVolume(", ".RestoreVolume(", ".EnsureVMLink(", ".DeleteVMLink("}
	hypervisorWrites := []string{".EnsureVM(", ".StopVM(", ".DeleteVM(", ".EnsureDisk(", ".DeleteDisk(", ".SnapshotDisk(", ".CopyDisk("}
	writes = append(writes, ".ConfigureBroker(", ".PurgeCache(", ".SetBucketPublicRead(", ".Transcode(", ".SetProjectStoreAccess(")
	// The broker's control and delivery methods belong to the reconciler too (ADR-029); services send,
	// receive, delete, peek, publish and read stats.
	brokerWrites := []string{".EnsureQueue(", ".DeleteQueue(", ".PurgeQueue(", ".EnsureTopic(", ".DeleteTopic(", ".Fetch(", ".DeadLetters(", ".MoveToDeadLetter("}
	check := func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, w := range writes {
			for _, field := range []string{"Engine", "Driver"} { // services call it Engine, the monitor Driver
				if strings.Contains(string(b), field+w) {
					t.Errorf("%s calls %s%s", path, field, w)
				}
			}
		}
		for _, w := range brokerWrites {
			if strings.Contains(string(b), w) {
				t.Errorf("%s calls the broker's %s, which only the reconciler may", path, w)
			}
		}
		for _, w := range hypervisorWrites {
			if strings.Contains(string(b), "Hypervisor"+w) {
				t.Errorf("%s calls Hypervisor%s", path, w)
			}
		}
		return nil
	}
	for _, dir := range []string{"internal/service", "internal/monitor"} {
		if err := filepath.WalkDir(filepath.Join(base, dir), check); err != nil {
			t.Fatal(err)
		}
	}
}
