# EnclaveStack platform — common tasks (L4-001, L4-002). Every target works from a clean clone.
GO        ?= go
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.8.0-dev)
LDFLAGS   := -s -w -X github.com/enclavestack/platform/internal/version.Version=$(VERSION)
BINDIR    ?=
TOFU      ?= $(shell command -v tofu 2>/dev/null)
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2

.PHONY: install tofu start demo build release console contracts embed lint test test-race integration provider-acceptance console-e2e engine-docker engine-qemu e2e e2e-machines linux-binary server-in-docker machine2 machine2-down load load-lb provider-dev clean

install: build ## put `enclave` on your PATH so any terminal can run it (BINDIR=/some/folder to choose where)
	@dir="$(BINDIR)"; \
	if [ -z "$$dir" ]; then for d in /opt/homebrew/bin /usr/local/bin; do if [ -d "$$d" ] && [ -w "$$d" ]; then dir="$$d"; break; fi; done; fi; \
	if [ -z "$$dir" ]; then echo "No writable folder on your PATH found. Pick one: make install BINDIR=\$$HOME/bin (and add it to PATH), or: sudo install -m 0755 build/enclave /usr/local/bin/enclave" >&2; exit 1; fi; \
	install -m 0755 build/enclave "$$dir/enclave" && echo "Installed $$dir/enclave. Open a new terminal, or run: hash -r. Then: enclave --help"

tofu: ## run OpenTofu against your EnclaveStack in one step; CMD=plan|apply|destroy, DIR=examples/opentofu, ARGS=-auto-approve
	examples/opentofu/run.sh

start: build ## build, then run the real server (Docker engine, data in ~/.enclave); ARGS='--listen 127.0.0.1:7171' adds flags
	./build/enclave server $(ARGS)

demo: build ## one command to a running server with sample data (fake engine; DRIVER=docker, PORT, DATA)
	examples/demo/run.sh

build: console embed ## console + both binaries in ./build (console embedded)
	$(GO) build -tags embedconsole -ldflags '$(LDFLAGS)' -o build/enclave ./cmd/enclave
	$(GO) build -ldflags '$(LDFLAGS)' -o build/terraform-provider-enclave ./cmd/terraform-provider-enclave

release: console embed ## every file of a release in ./build/release, for all four platforms (L4-002 §3); VERSION=vX.Y.Z
	$(GO) run ./cmd/release --version $(VERSION) --out build/release

console:
	pnpm --filter @enclavestack/console build

embed:
	rm -rf internal/webui/dist && cp -R console/dist internal/webui/dist

contracts: ## regenerate console/src/api/generated.ts from pkg/api, and the gate's stylesheet from packages/ui
	$(GO) run ./cmd/gencontracts
	cat packages/ui/src/tokens.css packages/ui/src/gate.css > internal/gate/static/gate.css

lint:
	$(GO) vet ./... && $(GO) vet -tags docker ./internal/engine/docker/ && $(GO) vet -tags qemu ./internal/engine/qemu/ && $(GO) vet -tags e2e ./tests/e2e/ && $(GO) vet -tags "e2e machines" ./tests/e2e/
	$(GO) run $(STATICCHECK) ./...
	pnpm --filter @enclavestack/console lint

test: ## unit + integration (real SQLite, fake engine), race detector
	$(GO) test -race -count=1 ./...
	pnpm --filter @enclavestack/console test

provider-acceptance: ## OpenTofu acceptance tests (needs tofu)
	TF_ACC=1 TF_ACC_TERRAFORM_PATH=$(TOFU) TF_ACC_PROVIDER_NAMESPACE=hashicorp TF_ACC_PROVIDER_HOST=registry.opentofu.org \
		$(GO) test -count=1 ./internal/provider/...

console-e2e: ## Playwright against the real server (needs Chrome)
	pnpm --filter @enclavestack/console e2e

engine-docker: ## Docker driver against real Docker
	$(GO) test -tags docker -count=1 -timeout 30m ./internal/engine/docker/...

engine-qemu: ## QEMU hypervisor against real QEMU (skips without QEMU or acceleration)
	$(GO) test -tags qemu -count=1 -timeout 20m ./internal/engine/qemu/...

e2e: ## built binaries + Docker + OpenTofu
	$(GO) test -tags e2e -count=1 -timeout 30m ./tests/e2e/...

e2e-machines: ## two machines: the server in a container and a Docker-in-Docker agent (L5-001 §2f); EDGE=1 joins an edge machine instead (§2h)
	$(GO) test -tags "e2e machines" -count=1 -timeout 20m -run $(if $(EDGE),TestEdgeMachine,TestTwoMachines) ./tests/e2e/

load: ## k6 load test against the fake engine (L5-003)
	tests/load/run.sh

load-lb: ## k6 through a load balancer during rolling deploys, on real Docker (L2-006 LB-06)
	tests/load/lb.sh

provider-dev: build ## point OpenTofu at ./build for enclavestack/enclave
	@printf 'provider_installation {\n  dev_overrides {\n    "enclavestack/enclave" = "%s"\n  }\n  direct {}\n}\n' "$(CURDIR)/build" > $(HOME)/.tofurc.enclave
	@echo "Wrote $(HOME)/.tofurc.enclave — export TF_CLI_CONFIG_FILE=$(HOME)/.tofurc.enclave"

# Many machines on one computer (L4-001 §v0.8): the server in a container and a Docker-in-Docker second
# machine, both reached through this Docker's bridge gateway.
DIND_IMAGE := docker:28.5.1-dind
GATEWAY     = $(shell docker network inspect bridge --format '{{(index .IPAM.Config 0).Gateway}}')
LINUX_ARCH  = $(shell docker info --format '{{.Architecture}}' | sed -e 's/aarch64/arm64/' -e 's/x86_64/amd64/')

linux-binary: console embed ## the Linux enclave binary for this Docker, in ./build/linux
	GOOS=linux GOARCH=$(LINUX_ARCH) CGO_ENABLED=0 $(GO) build -ldflags "$(LDFLAGS)" -o build/linux/enclave ./cmd/enclave

server-in-docker: linux-binary ## run the server in a container on this Docker (es-dev-server, port 7070)
	docker run -d --name es-dev-server -v /var/run/docker.sock:/var/run/docker.sock -v $(CURDIR)/build/linux/enclave:/usr/local/bin/enclave:ro \
		-v es-dev-data:/data -p 7070:7070 $(DIND_IMAGE) enclave server --listen 0.0.0.0:7070 --data-dir /data \
		--console-url http://$(GATEWAY):7070 --tunnel-address $(GATEWAY) --tunnel-port 51820
	@echo "Console: http://127.0.0.1:7070 — the setup code is in: docker logs es-dev-server"

machine2: linux-binary ## a Docker-in-Docker second machine (es-dev-machine2) and the command that joins it
	docker run -d --privileged --name es-dev-machine2 -e DOCKER_TLS_CERTDIR= -v $(CURDIR)/build/linux/enclave:/usr/local/bin/enclave:ro \
		-p 7071:7071 -p 51821:51821/udp $(DIND_IMAGE)
	@echo "Add it with: enclave machine add rack-1 — then run, with its code:"
	@echo "  docker exec -it es-dev-machine2 enclave agent join http://$(GATEWAY):7070 --code <code> --advertise $(GATEWAY) --tunnel-port 51821"

machine2-down: ## remove the second machine, with its engine's data
	docker rm -f -v es-dev-machine2

clean:
	rm -rf build internal/webui/dist console/dist
