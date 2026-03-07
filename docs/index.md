<p align="center">
  <img src="assets/novaroute-logo-dark.svg" alt="NovaRoute" width="480">
</p>

**Node-local, policy-safe routing control plane for Kubernetes.**

NovaRoute is a unified routing control service that centralizes BGP, BFD, and OSPF management on each Kubernetes node. It acts as the single owner of the [FRR (Free Range Routing)](https://frrouting.org/) daemon, exposing a gRPC API over a Unix domain socket so that multiple clients can safely share one routing stack without conflicting.

Clients such as [NovaEdge](https://github.com/piwi3910/novaedge) (load balancer VIPs), NovaNet (pod/node networking), and human administrators each declare routing intents through the API. NovaRoute validates every intent against ownership policies, reconciles the desired state into FRR via vtysh, and provides full observability through status queries and event streaming.

---

## Key Features

- **Unified routing stack** -- BGP, BFD, and OSPF managed through a single control plane per node, eliminating port conflicts and peer explosion
- **Intent-based API** -- Clients declare what they want (e.g., "advertise 10.0.0.1/32 via BGP"), not how to achieve it; NovaRoute handles the FRR translation
- **Policy-safe by default** -- Token authentication, prefix type validation (host_only, subnet, any), CIDR restrictions, and cross-owner conflict detection
- **13 gRPC RPCs** -- Full lifecycle management: Register, Deregister, ConfigureBGP, ApplyPeer, RemovePeer, AdvertisePrefix, WithdrawPrefix, EnableBFD, DisableBFD, EnableOSPF, DisableOSPF, GetStatus, StreamEvents
- **Intent-based reconciler** -- 30-second periodic sync plus triggered reconciliation with desired-vs-applied diffing and equality checks
- **Real-time event streaming** -- 15 event types with per-owner and per-type filtering, delivered over server-streaming gRPC
- **Prometheus metrics** -- gRPC call duration, policy violations, intent counts, and active sessions exported on `:9102`
- **Health check endpoints** -- `/healthz` (liveness) and `/readyz` (FRR connectivity check) for Kubernetes probes
- **Ephemeral state, durable routing** -- In-memory intent store with FRR graceful restart ensures zero traffic disruption across agent restarts
- **CLI tooling** -- `novaroutectl` provides both read-only inspection and full write operations against the agent
- **Production-ready deployment** -- Kubernetes DaemonSet with agent + FRR sidecar, hostNetwork, multi-arch Docker images (amd64/arm64), and optional Kubernetes operator for CRD-based configuration
- **Graceful shutdown** -- Withdraws all prefixes, BFD sessions, OSPF interfaces, and peers on SIGTERM with a 10-second timeout

---

## Quick Links

| Page | Description |
|------|-------------|
| [Getting Started](getting-started.md) | Prerequisites, installation, and your first BGP peer |
| [Architecture](architecture.md) | System design, component roles, data flow, and reconciliation details |
| [Configuration](configuration.md) | Full config reference, environment variables, and policy types |

---

## Project Info

| | |
|---|---|
| **Language** | Go 1.26 |
| **License** | Apache-2.0 |
| **Source** | [github.com/azrtydxb/NovaRoute](https://github.com/azrtydxb/NovaRoute) |
| **Docker Image** | `ghcr.io/azrtydxb/novaroute/novaroute-agent` (linux/amd64, linux/arm64) |
| **FRR Image** | `ghcr.io/azrtydxb/novaroute/novaroute-frr:10.5.1` |
| **Status** | Core routing features complete -- BGP, BFD, OSPF, reconciliation, policy engine, event streaming, Kubernetes operator |

---

## Related Projects

- **[NovaEdge](https://github.com/piwi3910/novaedge)** -- Kubernetes load balancer, reverse proxy, and SD-WAN gateway (primary consumer of NovaRoute)
- **NovaNet** -- Kubernetes CNI and pod networking (future consumer)
- **[FRR](https://frrouting.org/)** -- Free Range Routing, the routing engine NovaRoute controls
