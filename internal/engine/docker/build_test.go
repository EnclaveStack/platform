//go:build docker

package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hcbuild "github.com/enclavestack/platform/internal/build"
	"github.com/enclavestack/platform/internal/domain"
	"github.com/enclavestack/platform/internal/engine"
	"github.com/enclavestack/platform/pkg/api"
)

// Builds with real BuildKit through the Engine API (ADR-019, L5-002).

// contextOf writes files into a directory and checks it like the reconciler does.
func contextOf(t *testing.T, files map[string]string) (dir string, excludes []string) {
	t.Helper()
	dir = t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bc, err := hcbuild.PrepareContext(dir, ".", "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	return bc.Dir, bc.Excludes
}

func input(t *testing.T, app *domain.Resource, tag string, files map[string]string) engine.BuildInput {
	t.Helper()
	dir, excludes := contextOf(t, files)
	return engine.BuildInput{Resource: app, Tag: tag, ContextDir: dir, Dockerfile: "Dockerfile", Excludes: excludes}
}

func TestBuildWithBuildKitDeploysAndCleansUp(t *testing.T) {
	d := newDriver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	app := resource(api.KindApp, "it-built-"+suffix())
	cleanup(t, d, app, engine.ObjectStore{})
	dockerfile := `FROM busybox:1.37
ARG GREETING=none
RUN --mount=type=cache,target=/cache echo "greeting is $GREETING" && echo cached > /cache/marker
COPY site/ /www/
COPY big.txt /big.txt
CMD ["httpd", "-f", "-p", "8080", "-h", "/www"]
`
	var log bytes.Buffer
	tag := hcbuild.Tag(app.Name, 1, strings.Repeat("ab", 20))
	// A 2 MiB context: Docker Desktop's proxy truncated a chunked build body this size (ADR-019), so contexts
	// go through the build session. .dockerignore applies: secret.env never reaches the build.
	big := strings.Repeat("enclave build context padding\n", 64<<10)
	in := input(t, app, tag, map[string]string{"Dockerfile": dockerfile + "RUN test ! -e /www/secret.env && test -s /big.txt\n", "site/index.html": "built by enclave\n",
		"site/secret.env": "TOKEN=x\n", ".dockerignore": "**/*.env\n", "big.txt": big})
	in.Dockerfile, in.BuildArgs = "Dockerfile", map[string]string{"GREETING": "hello"}
	res, err := d.Build(ctx, in, &log)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, log.String())
	}
	if !strings.HasPrefix(res.ImageID, "sha256:") || !strings.Contains(log.String(), "greeting is hello") || !strings.Contains(log.String(), "RUN --mount=type=cache") {
		t.Errorf("result %+v, log:\n%s", res, log.String())
	}
	if ok, _ := d.ImageExists(ctx, tag); !ok {
		t.Fatal("built image missing")
	}

	// The built image deploys like any other, without a pull.
	out := ensure(t, d, engine.EnsureInput{Resource: app, Spec: api.AppSpec{Image: tag, Port: 8080, Publish: true, MemoryMiB: 64}, Revision: 1})
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%v/", out["port"]))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.TrimSpace(string(body)) != "built by enclave" {
		t.Errorf("served %q", body)
	}

	// A failing step is permanent and named.
	log.Reset()
	_, err = d.Build(ctx, input(t, app, hcbuild.Tag(app.Name, 2, strings.Repeat("cd", 20)), map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN echo testing && exit 3\n"}), &log)
	if !engine.IsPermanent(err) || !strings.Contains(err.Error(), "echo testing && exit 3") || !strings.Contains(err.Error(), "exit code: 3") || !strings.Contains(log.String(), "testing") {
		t.Errorf("failing build: %v\n%s", err, log.String())
	}

	// A missing built image is never pulled.
	gone := hcbuild.Tag(app.Name, 9, strings.Repeat("ef", 20))
	ctxE, cancelE := context.WithTimeout(ctx, time.Minute)
	defer cancelE()
	other := resource(api.KindApp, "it-gone-"+suffix())
	cleanup(t, d, other, engine.ObjectStore{})
	if _, err := d.Ensure(ctxE, engine.EnsureInput{Resource: other, Spec: api.AppSpec{Image: gone, MemoryMiB: 64}, Revision: 1}, &steps{}); !engine.IsPermanent(err) || !strings.Contains(err.Error(), "redeploy to build it again") {
		t.Errorf("missing built image: %v", err)
	}

	// Clean-up keeps what it is told to, and deleting the app removes the rest.
	second := hcbuild.Tag(app.Name, 3, strings.Repeat("12", 20))
	if _, err := d.Build(ctx, input(t, app, second, map[string]string{"Dockerfile": "FROM busybox:1.37\nRUN echo two\n"}), io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveBuiltImages(ctx, app, []string{second}); err != nil {
		t.Fatalf("clean-up with an image still in use: %v", err)
	}
	if ok, _ := d.ImageExists(ctx, tag); !ok {
		t.Error("an image a running revision uses was removed") // in use: kept until a later clean-up
	}
	if ok, _ := d.ImageExists(ctx, second); !ok {
		t.Error("the kept image was removed")
	}
	if err := d.Delete(ctx, app, engine.ObjectStore{}); err != nil {
		t.Fatal(err)
	}
	for _, img := range []string{tag, second} {
		if ok, _ := d.ImageExists(ctx, img); ok {
			t.Errorf("deleting the app left its built image %s", img)
		}
	}
}
