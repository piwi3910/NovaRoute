# NovaRoute Makefile
# Build automation for novaroute-agent and novaroutectl binaries.

BINARY_DIR    := bin
AGENT_BINARY  := $(BINARY_DIR)/novaroute-agent
CTL_BINARY    := $(BINARY_DIR)/novaroutectl
DOCKER_IMAGE  := ghcr.io/piwi3910/novaroute-agent
DOCKER_TAG    := latest

GO       := go
GOFLAGS  := -ldflags="-s -w"
GOTEST   := $(GO) test
GOVET    := $(GO) vet
PROTOC   := protoc

.PHONY: all build build-agent build-ctl test lint proto docker-build clean help

## build: Build both novaroute-agent and novaroutectl binaries
build: build-agent build-ctl

## build-agent: Build the novaroute-agent binary
build-agent:
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -o $(AGENT_BINARY) ./cmd/novaroute-agent/
	@echo "Built $(AGENT_BINARY)"

## build-ctl: Build the novaroutectl CLI binary
build-ctl:
	@mkdir -p $(BINARY_DIR)
	$(GO) build $(GOFLAGS) -o $(CTL_BINARY) ./cmd/novaroutectl/
	@echo "Built $(CTL_BINARY)"

## test: Run all tests with race detection
test:
	$(GOTEST) -race -count=1 ./...

## lint: Run go vet on all packages
lint:
	$(GOVET) ./...

## proto: Generate protobuf Go files from .proto definitions
proto:
	$(PROTOC) --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/v1/novaroute.proto

## docker-build: Build the Docker image
docker-build:
	docker build -t $(DOCKER_IMAGE):$(DOCKER_TAG) .

## clean: Remove build artifacts
clean:
	rm -rf $(BINARY_DIR)
	@echo "Cleaned build artifacts"

## help: Show this help message
help:
	@echo "Available targets:"
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## /  /'
