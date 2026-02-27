<p align="center">
  <h1 align="center">NovaRoute</h1>
  <p align="center">
    <strong>Node-local routing control plane for Kubernetes — BGP, BFD, and OSPF via FRR</strong>
  </p>
  <p align="center">
    <a href="https://github.com/piwi3910/NovaRoute/actions/workflows/ci.yml"><img src="https://github.com/piwi3910/NovaRoute/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
    <a href="https://github.com/piwi3910/NovaRoute/releases/latest"><img src="https://img.shields.io/github/v/release/piwi3910/NovaRoute?style=flat&color=blue" alt="Release"></a>
    <a href="https://pkg.go.dev/github.com/piwi3910/NovaRoute"><img src="https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white" alt="Go"></a>
    <a href="https://github.com/piwi3910/NovaRoute/pkgs/container/novaroute-agent"><img src="https://img.shields.io/badge/GHCR-amd64%20%7C%20arm64-purple?logo=docker" alt="Docker"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-green" alt="License"></a>
  </p>
</p>

---

NovaRoute is a unified routing control service that centralizes BGP, BFD, and OSPF management on each Kubernetes node. It acts as the single owner of the [FRR](https://frrouting.org/) routing daemon, exposing a gRPC API over a Unix domain socket so that multiple clients — load balancers, CNI plugins, and administrators — can safely share one routing stack without conflicting.

```
  NovaEdge    NovaNet     Admin (CLI)
     │           │            │
     └─────── gRPC (Unix Socket) ──────┐
                                        │
              ┌─────────────────────────▼──┐
              │      NovaRoute Agent       │
              │  Intent Store → Reconciler │
              └────────────┬───────────────┘
                           │ vtysh
              ┌────────────▼───────────────┐
              │       FRR Daemon           │
              │   BGP · BFD · OSPF         │
              └────────────────────────────┘
```

**[Full Documentation](https://piwi3910.github.io/NovaRoute)** · **[API Reference](https://piwi3910.github.io/NovaRoute/api-reference)** · **[CLI Reference](https://piwi3910.github.io/NovaRoute/cli-reference)**

---

## Features

- **Multi-protocol** — BGP (peers, prefixes, dynamic AS, soft-reconfigure, graceful-restart, max-prefix, prefix-lists), BFD (single-hop detection), OSPF (per-interface areas)
- **Intent-based** — Clients declare desired state; the reconciler diffs and applies to FRR
- **Multi-tenant** — Ownership model with per-client tokens, prefix-type validation, and conflict detection
- **Observable** — Prometheus metrics at `:9102`, real-time event streaming, health endpoints (`/healthz`, `/readyz`)
- **Production-ready** — Graceful shutdown, FRR state monitoring, periodic reconciliation, error recovery
- **Multi-arch** — Docker images for `linux/amd64` and `linux/arm64` published to GHCR

## Quick Start

### 1. Deploy to Kubernetes

```bash
# Apply the ConfigMaps and DaemonSet
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/daemonset.yaml
```

### 2. Configure BGP

```bash
# Register as an owner
novaroutectl register --owner myapp --token secret

# Add a BGP peer
novaroutectl apply-peer --owner myapp --token secret \
  --address 10.0.0.254 --remote-as 65001

# Advertise a prefix
novaroutectl advertise --owner myapp --token secret \
  --prefix 10.10.0.0/24
```

### 3. Check Status

```bash
novaroutectl status
novaroutectl peers
novaroutectl prefixes
```

## Configuration

NovaRoute reads a JSON config file (default: `/etc/novaroute/config.json`):

```json
{
  "listen_socket": "/run/novaroute/novaroute.sock",
  "frr": {
    "socket_dir": "/run/frr",
    "connect_timeout": 10,
    "retry_interval": 5
  },
  "bgp": {
    "local_as": 65000,
    "router_id": "10.0.0.1"
  },
  "owners": {
    "novaedge": {
      "token": "${NOVAEDGE_TOKEN}",
      "allowed_prefixes": {
        "type": "host_only",
        "allowed_cidrs": ["10.0.0.0/8"]
      }
    }
  },
  "log_level": "info",
  "metrics_address": ":9102"
}
```

Environment variable overrides: `NOVAROUTE_BGP_LOCAL_AS`, `NOVAROUTE_BGP_ROUTER_ID`. Token values support `${ENV_VAR}` expansion.

See the [Configuration Guide](https://piwi3910.github.io/NovaRoute/configuration) for all options.

## Architecture

```
┌────────────────────────────────────────────────┐
│               Kubernetes Node                   │
│                                                 │
│  Clients ──gRPC──▶ Server ──▶ Policy Engine    │
│                       │                         │
│                  Intent Store                   │
│                       │                         │
│                   Reconciler (30s + triggered)  │
│                       │                         │
│                  FRR Client (vtysh)             │
│                       │                         │
│                   FRR Daemon                    │
│              (bgpd, ospfd, zebra)              │
└────────────────────────────────────────────────┘
```

| Component | Role |
|-----------|------|
| **gRPC Server** | 14 RPCs over Unix socket — register, peers, prefixes, BFD, OSPF, status, events |
| **Policy Engine** | Token auth, prefix-type validation, CIDR restrictions, cross-owner conflict check |
| **Intent Store** | Thread-safe in-memory store of desired routing state per owner |
| **Reconciler** | Diffs desired vs applied state, generates FRR commands, monitors FRR state |
| **FRR Client** | Translates intents into `vtysh -c` batched commands |

See the [Architecture Guide](https://piwi3910.github.io/NovaRoute/architecture) for detailed component diagrams and data flow.

## Repository Structure

```
NovaRoute/
├── api/v1/                  # Protobuf/gRPC service definition
├── cmd/
│   ├── novaroute-agent/     # Main agent binary
│   └── novaroutectl/        # CLI tool
├── internal/
│   ├── config/              # JSON config loading + env var expansion
│   ├── frr/                 # FRR vtysh client (BGP, BFD, OSPF)
│   ├── intent/              # In-memory intent store
│   ├── metrics/             # Prometheus metrics
│   ├── policy/              # Ownership + prefix policy engine
│   ├── reconciler/          # State reconciliation + FRR monitoring
│   └── server/              # gRPC handlers + event streaming
├── deploy/                  # Kubernetes manifests (DaemonSet, ConfigMaps)
├── docs/                    # Documentation site (Jekyll)
├── .github/workflows/       # CI + release + docs deployment
├── Dockerfile               # Multi-stage build (Go + Alpine + FRR)
└── Makefile                 # Build automation
```

## Building

```bash
# Build binaries
make build

# Run tests
make test

# Build Docker image
make docker-build

# Regenerate protobuf (requires protoc)
make proto
```

## Contributing

We welcome contributions! See the [Contributing Guide](https://piwi3910.github.io/NovaRoute/contributing) for development setup, testing, and PR guidelines.

## License

Apache-2.0. See [LICENSE](LICENSE) for details. FRR itself is GPL-2.0.

## Related Projects

- **[NovaEdge](https://github.com/piwi3910/novaedge)** — Kubernetes load balancer, reverse proxy, and SD-WAN gateway (primary NovaRoute consumer)
- **NovaNet** — Kubernetes CNI and pod networking (future consumer)
- **[FRR](https://frrouting.org/)** — Free Range Routing, the routing engine NovaRoute controls
