.DEFAULT_GOAL := help

# ==================================================================================== #
# VARIABLES
# ==================================================================================== #
BINARY := p6e
BIN_DIR := bin

# ==================================================================================== #
# PHONY DECLARATIONS (in alphabetical order)
# ==================================================================================== #
.PHONY: audit bench build clean confirm fulltest help race release test tidy tools

# ==================================================================================== #
# STANDARD TARGETS (in alphabetical order)
# ==================================================================================== #

## audit: run quality control checks
audit:
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
	go build -o $(BIN_DIR)/$(BINARY) ./cmd/p6e

## clean: clean Go build and test cache and remove built binaries
clean:
	go clean -cache -testcache ./...
	rm -rf $(BIN_DIR)

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
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.11.3
	@go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
	@echo "Tools installed in $(shell go env GOBIN || go env GOPATH)/bin"

# ==================================================================================== #
# UTILITY TARGETS
# ==================================================================================== #

## confirm: prompt for user confirmation before proceeding
confirm:
	@echo -n 'Are you sure? [y/N] ' && read ans && [ $${ans:-N} = y ]
