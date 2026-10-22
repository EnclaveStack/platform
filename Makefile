# EnclaveStack platform — common tasks (L4-001, L4-002). Every target works from a clean clone.
GO        ?= go
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.2.0-dev)
LDFLAGS   := -s -w -X github.com/enclavestack/platform/internal/version.Version=$(VERSION)
TOFU      ?= $(shell command -v tofu 2>/dev/null)
STATICCHECK := honnef.co/go/tools/cmd/staticcheck@v0.8.2

.PHONY: build console contracts embed lint test test-race integration provider-acceptance console-e2e engine-docker engine-qemu e2e load load-lb provider-dev clean

build: console embed ## console + both binaries in ./build (console embedded)
	$(GO) build -tags embedconsole -ldflags '$(LDFLAGS)' -o build/enclave ./cmd/enclave
	$(GO) build -ldflags '$(LDFLAGS)' -o build/terraform-provider-enclave ./cmd/terraform-provider-enclave

console:
	pnpm --filter @enclavestack/console build

embed:
	rm -rf internal/webui/dist && cp -R console/dist internal/webui/dist

contracts: ## regenerate console/src/api/generated.ts from pkg/api
	$(GO) run ./cmd/gencontracts

lint:
	$(GO) vet ./... && $(GO) vet -tags docker ./internal/engine/docker/ && $(GO) vet -tags qemu ./internal/engine/qemu/ && $(GO) vet -tags e2e ./tests/e2e/
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
	$(GO) test -tags docker -count=1 ./internal/engine/docker/...

engine-qemu: ## QEMU hypervisor against real QEMU (skips without QEMU or acceleration)
	$(GO) test -tags qemu -count=1 -timeout 20m ./internal/engine/qemu/...

e2e: ## built binaries + Docker + OpenTofu
	$(GO) test -tags e2e -count=1 -timeout 30m ./tests/e2e/...

load: ## k6 load test against the fake engine (L5-003)
	tests/load/run.sh

load-lb: ## k6 through a load balancer during rolling deploys, on real Docker (L2-006 LB-06)
	tests/load/lb.sh

provider-dev: build ## point OpenTofu at ./build for enclavestack/enclave
	@printf 'provider_installation {\n  dev_overrides {\n    "enclavestack/enclave" = "%s"\n  }\n  direct {}\n}\n' "$(CURDIR)/build" > $(HOME)/.tofurc.enclave
	@echo "Wrote $(HOME)/.tofurc.enclave — export TF_CLI_CONFIG_FILE=$(HOME)/.tofurc.enclave"

clean:
	rm -rf build internal/webui/dist console/dist
