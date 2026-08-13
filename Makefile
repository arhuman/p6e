.DEFAULT_GOAL := help

# ==================================================================================== #
# VARIABLES
# ==================================================================================== #
BINARY := p6e
BIN_DIR := bin

ENV_FILE ?= .env

# `docker compose` reads .env on its own, but `make` does not: -include it and
# export so recipe shells (the preflight domain guard below) see the real
# values. The leading `-` keeps a missing .env from erroring; preflight is what
# catches that.
-include $(ENV_FILE)
export

# Host port the local stack publishes. Exported so compose interpolation
# (${P6E_PORT:-8080}) resolves to the same value the local target prints.
# Override per run: make local P6E_PORT=9090
P6E_PORT ?= 8080
export P6E_PORT

BASE_COMPOSE  := docker compose --env-file $(ENV_FILE) -f docker-compose.yml
LOCAL_COMPOSE := $(BASE_COMPOSE) -f docker-compose.local.yml
PROD_COMPOSE  := $(BASE_COMPOSE) -f docker-compose.prod.yml

# Quality gate. A ratchet: raise it as coverage improves, never lower it to
# green a build.
COVER_MIN ?= 85

# Pinned tool versions. These must equal the versions the CI lint job installs,
# or the two disagree about what passes.
GOLANGCI_VERSION    ?= v2.12.2
GOVULNCHECK_VERSION ?= v1.1.4

# Version metadata stamped into the binary, so a running daemon can say which
# build it is. BUILD_DATE is the committer date, not wall-clock time, which
# keeps two builds of the same commit byte-identical.
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell git log -1 --format=%cI 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X 'main.Version=$(VERSION)' \
	-X 'main.GitCommit=$(COMMIT)' \
	-X 'main.BuildDate=$(BUILD_DATE)'

# ==================================================================================== #
# PHONY DECLARATIONS (in alphabetical order)
# ==================================================================================== #
.PHONY: audit bench build ci clean confirm cover down fulltest help image local logs preflight race release test tidy tools up

# ==================================================================================== #
# STANDARD TARGETS (in alphabetical order)
# ==================================================================================== #

## audit: run quality control checks (mod verify, vet, lint, vuln scan, coverage gate)
audit: cover
	@which golangci-lint > /dev/null || $(MAKE) tools
	@which govulncheck > /dev/null || $(MAKE) tools
	go mod verify
	go vet ./...
	golangci-lint run ./...
	govulncheck ./...

## bench: run engine overhead benchmarks
bench:
	go test -bench=. -benchmem -run='^$$' ./internal/runtime/...

## build: compile the p6e binary into bin/ with version metadata
# CGO_ENABLED=0 => a static binary that runs in scratch/alpine; -trimpath =>
# reproducible paths.
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/p6e

## ci: run the full local gate (tidy, audit, race)
# Deliberately not `tidy audit fulltest`: audit already runs the whole suite
# through cover, so adding fulltest would be a third full run for nothing. race
# is here because it is the one pass that tests something the others cannot.
ci: tidy audit race

## clean: clean Go build and test cache and remove built binaries
clean:
	go clean -cache -testcache ./...
	rm -rf $(BIN_DIR) coverage.out

## cover: run tests with coverage and fail below COVER_MIN
# -coverpkg=./... counts the production code a test exercises rather than only
# the package it lives in, which is what makes a helper package such as
# internal/nodes/jsonpath count where it is actually used.
#
# It also changes what `go test` prints per package: each line becomes that
# package's contribution to the whole, so a healthy package can read as 3%.
# Ignore those; the figure this gate uses is the "coverage:" total below them.
cover:
	go test -covermode=atomic -coverpkg=./... -coverprofile=coverage.out ./...
	@go tool cover -func=coverage.out | awk '/^total:/ {print "coverage: " $$3}'
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {print $$3}' | tr -d '%'); \
	awk -v t="$$total" -v min="$(COVER_MIN)" 'BEGIN { if (t+0 < min+0) { printf "FAIL: coverage %.1f%% < %d%%\n", t, min; exit 1 } }'

## down: stop and remove the local stack
down:
	$(LOCAL_COMPOSE) down

## fulltest: run *all* tests with verbose output. use 'test' for short/unit tests only.
fulltest:
	go test -v -cover ./...

## help: display this help message
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## image: build the container image with the same version metadata as build
image:
	DOCKER_BUILDKIT=1 docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		-t $(BINARY):$(VERSION) .

## local: start the local dev stack (published ports, fails fast)
# The env file is created here rather than by a `$(ENV_FILE):` rule, and that is
# load bearing. GNU make remakes any included makefile it has a rule for, so
# `-include $(ENV_FILE)` plus such a rule would auto-create .env from the
# template for *every* target, `up` and `preflight` included, and then restart.
# The guard against booting prod on template defaults would silently never fire.
local:
	@test -f $(ENV_FILE) || { cp env.sample $(ENV_FILE); echo "created $(ENV_FILE) from env.sample"; }
	$(LOCAL_COMPOSE) up -d --build
	@echo "local stack up: http://localhost:$(P6E_PORT)  (admin on http://127.0.0.1:$${P6E_ADMIN_PORT:-8081})"

## logs: follow the local stack's logs
logs:
	$(LOCAL_COMPOSE) logs -f

## preflight: fail unless .env holds production-ready values (gates `make up`)
# Deliberately NOT a $(ENV_FILE) prerequisite: prod must never boot on template
# defaults, so a missing .env is a hard failure rather than an auto-created
# placeholder. That is what `make local` is for. The guards below rely on the
# top-level `-include $(ENV_FILE)` + `export`, because make does not read .env.
#
# p6e has no admin password or signing secret to check: its only unsafe default
# is the domain Traefik routes, and serving a stranger's hostname is the failure
# this exists to prevent.
preflight:
	@test -f $(ENV_FILE) || { echo "$(ENV_FILE) missing: cp env.sample $(ENV_FILE) and fill it before prod" >&2; exit 1; }
	@bad=""; \
	[ -n "$$APEX_DOMAIN" ]                  || bad="$$bad\n  APEX_DOMAIN is empty (Traefik Host() rules need it)"; \
	[ "$$APEX_DOMAIN" != example.invalid ]  || bad="$$bad\n  APEX_DOMAIN is still the env.sample default"; \
	[ "$$APEX_DOMAIN" != example.com ]      || bad="$$bad\n  APEX_DOMAIN is still a placeholder"; \
	[ -d "$${P6E_PIPELINES:-./examples}" ]  || bad="$$bad\n  P6E_PIPELINES does not exist: $${P6E_PIPELINES}"; \
	case "$${P6E_PIPELINES:-./examples}" in ./examples|examples) \
	  bad="$$bad\n  P6E_PIPELINES still points at the shipped examples, which include a deliberately broken pipeline and are not a deployment";; \
	esac; \
	if [ -n "$$bad" ]; then printf "preflight failed:$$bad\n" >&2; exit 1; fi; \
	echo "preflight OK: $(ENV_FILE) looks production-ready"
	@$(MAKE) --no-print-directory build
	@./$(BIN_DIR)/$(BINARY) check --dir "$${P6E_PIPELINES:-./examples}"

## race: run tests under the race detector
race:
	go test -race ./...

## release: cut and publish a release (derive version, gate, tag, push)
# Pushing the v* tag is what triggers .github/workflows/release.yml and
# goreleaser. The gate lives in `ci`, which this runs; it is not repeated here.
release:
	@./scripts/release.sh

## test: run short tests with coverage (use fulltest to include long tests)
test:
	go test -short -cover ./...

## tidy: format Go code and tidy the module file
tidy:
	go fmt ./...
	go mod tidy -v

## tools: install required Go development tools
tools:
	@echo "Installing Go tools..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)
	@go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@echo "Tools installed in $(shell go env GOBIN || go env GOPATH)/bin"

## up: start the production stack in detached mode (gated by preflight)
up: preflight
	$(PROD_COMPOSE) up -d --build
	@echo "prod stack up: routed via Traefik on www.$${APEX_DOMAIN}"

# ==================================================================================== #
# UTILITY TARGETS
# ==================================================================================== #

## confirm: prompt for user confirmation before proceeding
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]
