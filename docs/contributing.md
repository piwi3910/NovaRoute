---
layout: default
title: Contributing
nav_order: 10
---

# Contributing

This guide covers how to set up a development environment, build and test NovaRoute, and submit contributions.

## Prerequisites

- **Go 1.26+** -- NovaRoute uses modern Go features and requires Go 1.26 or later.
- **protoc** -- The Protocol Buffers compiler for generating gRPC code.
- **protoc-gen-go** -- Go code generator plugin for protobuf.
- **protoc-gen-go-grpc** -- Go gRPC code generator plugin.
- **make** -- GNU Make for build automation.
- **Docker** -- Required for building container images.

Install the protobuf Go plugins:

```bash
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

## Clone and Build

```bash
git clone https://github.com/<org>/novaroute.git
cd novaroute
make build
```

This produces two binaries:

- `bin/novaroute-agent` -- The main routing control plane agent.
- `bin/novaroutectl` -- The CLI tool for interacting with the agent.

## Make Targets

| Target | Command | Description |
|--------|---------|-------------|
| `make build` | `go build` | Build agent and CLI binaries into `bin/`. |
| `make test` | `go test -race -count=1 ./...` | Run all tests with the race detector enabled. |
| `make lint` | `go vet ./...` | Run static analysis checks. |
| `make proto` | `protoc` with `go_out` and `go-grpc_out` | Regenerate Go code from `.proto` files. |
| `make docker-build` | `docker build` | Build the container image. |

## Running Tests

```bash
make test
```

This runs `go test -race -count=1 ./...`, which:

- Executes all tests across all packages.
- Enables the Go race detector to catch data races.
- Disables test result caching (`-count=1`) so every run is fresh.

All tests must pass before submitting a pull request.

## Running the Linter

```bash
make lint
```

This runs `go vet ./...` to catch common Go mistakes. All code must be `go vet` clean.

## Proto Generation

If you modify any `.proto` files, regenerate the Go code:

```bash
make proto
```

This runs `protoc` with the `--go_out` and `--go-grpc_out` flags to produce updated Go types and gRPC service stubs. Always commit the generated code alongside your proto changes.

## Docker Build

```bash
make docker-build
```

This builds a container image suitable for deployment as a Kubernetes DaemonSet alongside an FRR sidecar.

## Project Structure

```
novaroute/
├── cmd/
│   ├── novaroute-agent/    # Agent entry point
│   └── novaroutectl/       # CLI entry point
├── api/
│   └── proto/              # Protobuf service definitions
├── internal/
│   ├── config/             # Configuration loading and validation
│   ├── frr/                # FRR client (vtysh interaction)
│   ├── intent/             # Intent store and management
│   ├── metrics/            # Prometheus metrics registration
│   ├── policy/             # Policy engine (ownership, CIDR checks)
│   ├── reconciler/         # Reconciliation loop (intent -> FRR state)
│   └── server/             # gRPC server implementation
├── bin/                    # Build output directory
├── docs/                   # Documentation (this site)
├── Makefile
├── Dockerfile
└── go.mod
```

### Key Packages

- **`internal/config`** -- Loads and validates the JSON configuration file, including owner policies, socket paths, and metric settings.
- **`internal/frr`** -- Communicates with FRR via vtysh commands over UNIX sockets. Handles command generation, execution, and output parsing.
- **`internal/intent`** -- The intent store holds the desired routing state (peers, prefixes, BFD sessions, OSPF interfaces) keyed by owner.
- **`internal/metrics`** -- Registers and exposes all Prometheus counters, gauges, and histograms.
- **`internal/policy`** -- Evaluates ownership, token authentication, prefix restrictions, and operation permissions before intents are accepted.
- **`internal/reconciler`** -- Periodically compares the intent store against actual FRR state and applies the necessary changes to converge.
- **`internal/server`** -- Implements the gRPC service RPCs including intent management, status queries, and event streaming.

## Code Style

- **Formatting:** All code must be formatted with `gofmt`. Most editors handle this automatically.
- **Static analysis:** Code must pass `go vet ./...` with no findings.
- **No CGO:** NovaRoute has no CGO dependencies. Builds are pure Go for simple cross-compilation and static linking.
- **Error handling:** Wrap errors with context using `fmt.Errorf("operation: %w", err)`. Never silently discard errors.
- **Naming:** Follow standard Go naming conventions. Exported names use PascalCase, unexported names use camelCase.

## Testing Patterns

### Table-Driven Tests

NovaRoute uses table-driven tests extensively:

```go
func TestValidateHoldTime(t *testing.T) {
    tests := []struct {
        name      string
        keepalive int
        holdTime  int
        wantErr   bool
    }{
        {"valid", 10, 30, false},
        {"hold too low", 10, 20, true},
        {"zero values", 0, 0, false},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validateHoldTime(tt.keepalive, tt.holdTime)
            if (err != nil) != tt.wantErr {
                t.Errorf("validateHoldTime(%d, %d) error = %v, wantErr %v",
                    tt.keepalive, tt.holdTime, err, tt.wantErr)
            }
        })
    }
}
```

### Fake vtysh Scripts

The FRR client tests use fake vtysh shell scripts that simulate FRR responses. These scripts are placed in test fixtures and configured via the FRR client's command path. This allows testing the full command generation and output parsing pipeline without a real FRR instance.

### Race Detector

All tests run with `-race` enabled. If you introduce concurrency (goroutines, shared state, channels), the race detector will catch unsafe access patterns. Fix all races before submitting.

## CI Pipeline

Every pull request triggers the following CI steps:

1. **Lint** -- `make lint` runs `go vet` to catch static analysis issues.
2. **Test** -- `make test` runs the full test suite with race detection.
3. **Build** -- `make build` verifies the project compiles cleanly.
4. **Docker Build** -- `make docker-build` ensures the container image builds.
5. **Security Scan** -- `govulncheck` checks for known vulnerabilities in dependencies.

All steps must pass before a PR can be merged.

## Release Process

Releases are triggered by pushing a Git tag with a `v` prefix:

```bash
git tag v0.3.0
git push origin v0.3.0
```

The release pipeline:

1. Builds multi-architecture binaries (linux/amd64, linux/arm64).
2. Builds and pushes a multi-arch Docker image to GitHub Container Registry (GHCR).
3. Creates a GitHub Release with the compiled binaries attached.

## Pull Request Process

1. **Fork** the repository and clone your fork.
2. **Create a branch** from `main` with a descriptive name (e.g. `fix-bgp-timer-validation` or `add-ospf-area-support`).
3. **Write tests** for your changes. New features require tests; bug fixes should include a regression test.
4. **Run the checks locally:**
   ```bash
   make lint
   make test
   make build
   ```
5. **Commit** with clear, descriptive commit messages.
6. **Push** your branch to your fork.
7. **Open a pull request** against `main`. Describe what the change does and why.
8. **Ensure CI passes.** All lint, test, build, and security checks must be green.
9. **Address review feedback** promptly. Push additional commits rather than force-pushing.

Thank you for contributing to NovaRoute.
