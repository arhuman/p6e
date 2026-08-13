.DEFAULT_GOAL := help

# ==================================================================================== #
# VARIABLES
# ==================================================================================== #
BINARY := p6e
BIN_DIR := bin


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
.PHONY: audit bench build ci clean confirm cover down fulltest help image logs race release test tidy tools up

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

## down: stop the local stack
down:
	docker compose -f docker-compose.yml -f docker-compose.local.yml down

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

## logs: follow the local stack's logs
logs:
	docker compose -f docker-compose.yml -f docker-compose.local.yml logs -f

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

## up: build and run the local stack
up:
	docker compose -f docker-compose.yml -f docker-compose.local.yml up --build -d

# ==================================================================================== #
# UTILITY TARGETS
# ==================================================================================== #

## confirm: prompt for user confirmation before proceeding
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]
