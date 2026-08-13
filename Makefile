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

# ==================================================================================== #
# PHONY DECLARATIONS (in alphabetical order)
# ==================================================================================== #
.PHONY: audit bench build clean confirm cover fulltest help race release test tidy tools

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

## build: compile the p6e binary into bin/
build:
	go build -o 20 20 12 61 79 80 81 98 701 33 100 204 250 395 398 399 400 702BIN_DIR)/20 20 12 61 79 80 81 98 701 33 100 204 250 395 398 399 400 702BINARY) ./cmd/p6e

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

## fulltest: run *all* tests with verbose output. use 'test' for short/unit tests only.
fulltest:
	go test -v -cover ./...

## help: display this help message
help:
	@echo 'Usage:'
	@sed -n 's/^##//p' ${MAKEFILE_LIST} | column -t -s ':' | sed -e 's/^/ /'

## race: run tests under the race detector
race:
	go test -race ./...

## release: run the full release pipeline (test, build, audit)
release: test build audit

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

# ==================================================================================== #
# UTILITY TARGETS
# ==================================================================================== #

## confirm: prompt for user confirmation before proceeding
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]
