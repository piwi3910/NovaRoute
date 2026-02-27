# NovaRoute

**Node-local, policy-safe routing control plane for Kubernetes.**

NovaRoute is a unified routing control service that centralizes BGP, BFD, and OSPF management on each Kubernetes node. It acts as a single owner of the FRR (Free Range Routing) daemon, exposing a gRPC API over a Unix domain socket so that multiple clients — NovaEdge (load balancer VIPs), NovaNet (pod/node networking), and human administrators — can safely share one routing stack without conflicting.

> **Status:** Core routing features complete — BGP (peers, prefixes, dynamic AS), BFD, OSPF, intent-based reconciliation, policy engine, and event streaming are implemented and deployed in production. See [Feature Status](#feature-status) for details.

---

## Table of Contents

- [Problem Statement](#problem-statement)
- [Architecture Overview](#architecture-overview)
- [Core Design Principles](#core-design-principles)
- [FRR Integration](#frr-integration)
- [API Surface](#api-surface)
- [Ownership Model](#ownership-model)
- [State Model](#state-model)
- [Deployment Model](#deployment-model)
- [Configuration](#configuration)
- [CLI](#cli)
- [Feature Status](#feature-status)
- [Future Work](#future-work)

---

## Problem Statement

In a typical bare-metal Kubernetes cluster, multiple components need to advertise routes via BGP, monitor link health via BFD, and participate in OSPF areas:

| Component | What It Advertises | Protocol |
|-----------|-------------------|----------|
| **NovaEdge** (load balancer) | Virtual IP /32 addresses | BGP, OSPF |
| **NovaNet** (CNI / pod networking) | Pod CIDR subnets, node CIDRs | BGP |
| **Admin** | Static routes, custom prefixes | Any |

Today, each component embeds its own routing library (e.g., GoBGP), leading to:

1. **Port conflicts** — only one process can bind TCP 179 (BGP)
2. **Peer explosion** — each component establishes its own BGP sessions to the same routers
3. **No coordination** — components can advertise conflicting routes or tear down each other's sessions
4. **Duplicated complexity** — every component re-implements BGP session management, BFD, graceful restart, etc.
5. **Operational opacity** — no single place to inspect "what is this node advertising?"

NovaRoute solves all of these by owning FRR as the single routing engine and providing a controlled, policy-enforced API for all clients.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│                   Kubernetes Node                    │
│                                                      │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐           │
│  │ NovaEdge │  │ NovaNet  │  │  Admin   │           │
│  │  Agent   │  │  Agent   │  │  (CLI)   │           │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘           │
│       │              │              │                 │
│       │    Unix Socket gRPC         │                 │
│       │   /run/novaroute/novaroute.sock              │
│       │              │              │                 │
│  ┌────▼──────────────▼──────────────▼─────┐          │
│  │            NovaRoute Agent              │          │
│  │                                         │          │
│  │  ┌─────────────────────────────────┐   │          │
│  │  │  Intent Store (in-memory)       │   │          │
│  │  │  - owner → peers/prefixes      │   │          │
│  │  │  - BFD/OSPF sessions           │   │          │
│  │  └─────────────────────────────────┘   │          │
│  │                                         │          │
│  │  ┌─────────────────────────────────┐   │          │
│  │  │  Policy Engine                  │   │          │
│  │  │  - token authentication         │   │          │
│  │  │  - prefix type validation       │   │          │
│  │  │  - cross-owner conflict check   │   │          │
│  │  └─────────────────────────────────┘   │          │
│  │                                         │          │
│  │  ┌─────────────────────────────────┐   │          │
│  │  │  Reconciler                     │   │          │
│  │  │  - desired vs applied diffing   │   │          │
│  │  │  - periodic + triggered sync    │   │          │
│  │  └──────────────┬──────────────────┘   │          │
│  │                 │                      │          │
│  │  ┌──────────────▼──────────────────┐   │          │
│  │  │  FRR Client (vtysh CLI)         │   │          │
│  │  │  - configure terminal batches   │   │          │
│  │  │  - show commands for status     │   │          │
│  │  └──────────────┬──────────────────┘   │          │
│  └─────────────────┼──────────────────────┘          │
│                    │                                  │
│                    │ vtysh over VTY Unix sockets      │
│                    │ (/run/frr/zebra.vty, bgpd.vty)  │
│                    │                                  │
│  ┌─────────────────▼──────────────────────┐          │
│  │              FRR Daemon                 │          │
│  │  (bgpd, ospfd, zebra, mgmtd)          │          │
│  │                                         │          │
│  │  TCP 179 ──── BGP sessions ──── Routers│          │
│  │  BFD ──────── Link detection ────── ↑  │          │
│  │  OSPF ─────── Area adjacencies ──── ↑  │          │
│  └─────────────────────────────────────────┘          │
└─────────────────────────────────────────────────────┘
```

### Component Roles

| Component | Role |
|-----------|------|
| **NovaRoute Agent** | Accepts intents from clients via gRPC, enforces ownership policies, reconciles desired state to FRR via vtysh CLI commands |
| **FRR** | Production-grade routing suite. Owns all BGP sessions (TCP 179), BFD sessions, OSPF adjacencies. |
| **Clients** (NovaEdge, NovaNet, Admin) | Submit routing intents (advertise prefix, establish peer, enable BFD) via Unix socket gRPC. Never touch FRR directly. |

---

## Core Design Principles

### 1. Single Owner of the Routing Stack

FRR is a shared, stateful resource. NovaRoute is its sole controller. No other process on the node configures FRR directly. This eliminates conflicts, simplifies debugging, and enables policy enforcement.

### 2. Intent-Based, Not Imperative

Clients declare **what** they want ("advertise 10.0.0.1/32 via BGP"), not **how** to achieve it. NovaRoute translates intents into FRR vtysh commands. This decouples clients from FRR internals.

### 3. Policy-Safe by Default

Every intent is validated against ownership rules before reaching FRR:
- NovaEdge can only advertise /32 VIP addresses
- NovaNet can only advertise pod/node CIDR subnets
- Overlap between owners is rejected (admin can override)

### 4. Ephemeral State, Durable Routing

NovaRoute stores intents in memory only. On restart, clients re-assert their intents (they already have the source of truth — e.g., NovaEdge knows its VIP assignments). FRR's graceful restart holds existing routes in the kernel during the gap, ensuring zero traffic disruption.

### 5. Observable

A single `novaroutectl status` command shows everything the node is advertising, all peer sessions, BFD status, and which client owns each route.

---

## FRR Integration

### Why FRR?

| Criteria | FRR | GoBGP | BIRD |
|----------|-----|-------|------|
| BGP | Full (iBGP, eBGP, ECMP, communities, route maps) | Good | Full |
| BFD | Native (bfdd) | None | None |
| OSPF | Native (ospfd/ospf6d) | None | Full |
| Graceful Restart | Full | Partial | Full |
| Production track record | Massive (datacenters, ISPs) | Moderate | Good |
| License | GPL-2.0 | Apache-2.0 | GPL-2.0 |

FRR is the only option that provides BGP + BFD + OSPF in a single daemon. It's the industry standard for software-defined routing on Linux.

### How NovaRoute Controls FRR

NovaRoute controls FRR via **vtysh** (the FRR unified shell) over **VTY Unix sockets**. Each FRR daemon creates a `<daemon>.vty` socket in `/run/frr/` that vtysh connects to.

```
NovaRoute Agent
    │
    ├── vtysh --vty_socket /run/frr -c "show bgp summary"     (show commands)
    │
    └── vtysh --vty_socket /run/frr -f /tmp/batch.conf         (config batches)
            │
            ├── configure terminal
            ├── router bgp 65011
            ├──   neighbor 192.168.100.1 remote-as 65000
            ├──   address-family ipv4 unicast
            ├──     network 192.168.100.10/32
            ├──   exit-address-family
            ├── end
            └── (applied atomically by vtysh)
```

**Why vtysh instead of FRR's northbound gRPC/YANG API?**

| Approach | Pros | Cons |
|----------|------|------|
| **vtysh CLI** (current) | No extra dependencies, works with stock FRR, simple to debug, reliable | Text output parsing, no transactional candidate/commit |
| FRR northbound gRPC | YANG-modeled, transactional commits | Requires mgmtd with gRPC compiled, complex protobuf, immature API surface |

We chose vtysh for reliability and simplicity. The reconciler's desired-vs-applied diffing provides equivalent consistency guarantees — if a vtysh command fails, the intent remains in the desired state and will be retried on the next reconciliation cycle (every 30 seconds).

### Implemented FRR Operations

| Operation | vtysh Commands |
|-----------|---------------|
| **BGP global setup** | `router bgp <AS>`, `bgp router-id <ID>` |
| **BGP AS change** | `no router bgp <old>`, `router bgp <new>` (tears down sessions, reconciler re-applies) |
| **Add BGP peer** | `neighbor <addr> remote-as <AS>`, timers, eBGP-multihop, password |
| **Activate AFI** | `address-family ipv4/ipv6 unicast`, `neighbor <addr> activate` |
| **Advertise prefix** | `network <prefix>` under address-family (auto-detects IPv4/IPv6) |
| **Withdraw prefix** | `no network <prefix>` |
| **Add BFD session** | `bfd`, `peer <addr>`, receive/transmit intervals, detect-multiplier |
| **Enable OSPF** | `ip ospf area <area>` on interface, optional cost/hello/dead timers, passive mode |
| **FRR readiness** | Checks for `zebra.vty` + `bgpd.vty` sockets in `/run/frr/` |

---

## API Surface

NovaRoute exposes a gRPC API over a Unix domain socket at `/run/novaroute/novaroute.sock`.

### Service Definition

All 14 RPCs are fully implemented:

```protobuf
service RouteControl {
  // Session management
  rpc Register(RegisterRequest) returns (RegisterResponse);
  rpc Deregister(DeregisterRequest) returns (DeregisterResponse);

  // BGP global configuration (dynamic AS/router-id at runtime)
  rpc ConfigureBGP(ConfigureBGPRequest) returns (ConfigureBGPResponse);

  // Peer management
  rpc ApplyPeer(ApplyPeerRequest) returns (ApplyPeerResponse);
  rpc RemovePeer(RemovePeerRequest) returns (RemovePeerResponse);

  // Prefix advertisement
  rpc AdvertisePrefix(AdvertisePrefixRequest) returns (AdvertisePrefixResponse);
  rpc WithdrawPrefix(WithdrawPrefixRequest) returns (WithdrawPrefixResponse);

  // BFD
  rpc EnableBFD(EnableBFDRequest) returns (EnableBFDResponse);
  rpc DisableBFD(DisableBFDRequest) returns (DisableBFDResponse);

  // OSPF
  rpc EnableOSPF(EnableOSPFRequest) returns (EnableOSPFResponse);
  rpc DisableOSPF(DisableOSPFRequest) returns (DisableOSPFResponse);

  // Observability
  rpc GetStatus(GetStatusRequest) returns (GetStatusResponse);
  rpc StreamEvents(StreamEventsRequest) returns (stream RouteEvent);
}
```

### Key Types

**BGP Peer** — full configuration including remote-as, peer type (eBGP/iBGP), timers, address families (IPv4/IPv6 unicast), eBGP-multihop, source address, and password.

**Prefix Attributes** — local-preference, communities, MED, next-hop override.

**BFD Session** — per-peer with configurable min-rx/min-tx (milliseconds) and detect-multiplier. Optional interface binding.

**OSPF Interface** — per-interface with area ID, cost, hello/dead intervals, and passive mode.

**Events** — 13 event types including peer up/down, prefix advertised/withdrawn, BFD up/down, OSPF neighbor changes, FRR connection state, owner registration, and policy violations. Supports filtering by owner and event type.

### Key API Behaviors

1. **Idempotent operations** — calling `AdvertisePrefix` for an already-advertised prefix is a no-op
2. **Owner scoping** — all mutations require an owner field; withdrawing another owner's prefix is rejected
3. **Token authentication** — every RPC validates the owner's pre-shared token before processing
4. **Policy enforcement** — prefix type, CIDR restrictions, and cross-owner conflict detection are checked before storing intents
5. **Event streaming** — `StreamEvents` provides filtered, non-blocking event delivery with per-subscriber buffered channels
6. **Session registration** — on (re)connect, clients call `Register` to get their current state and optionally re-assert all intents

---

## Ownership Model

The ownership model is the core safety mechanism. It prevents clients from interfering with each other's routes.

### Ownership Boundaries

| Owner | Allowed Prefixes | Restrictions |
|-------|-----------------|-------------|
| **novaedge** | /32 (IPv4) and /128 (IPv6) host routes only | Cannot advertise subnets; optional CIDR allowlist |
| **novanet** | Subnet prefixes (/8 - /28) | Cannot advertise host routes; optional CIDR allowlist |
| **admin** | Any prefix length | No restrictions, can override conflicts |

### Conflict Resolution

1. **Same prefix, same owner** — idempotent update (latest attributes win)
2. **Same prefix, different owners** — **rejected** with error, unless the requesting owner is `admin`
3. **Admin override** — admin-owned prefixes always succeed, even if conflicting with another owner

### Configuration

Each owner authenticates with a pre-shared token:

```json
{
  "owners": {
    "novaedge": {
      "token": "${NOVAEDGE_TOKEN}",
      "allowed_prefixes": {
        "type": "host_only",
        "allowed_cidrs": ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]
      }
    },
    "novanet": {
      "token": "${NOVANET_TOKEN}",
      "allowed_prefixes": {
        "type": "subnet",
        "allowed_cidrs": ["10.244.0.0/16"]
      }
    },
    "admin": {
      "token": "${ADMIN_TOKEN}",
      "allowed_prefixes": {
        "type": "any"
      }
    }
  }
}
```

---

## State Model

### Intent-Based, No Disk Persistence

NovaRoute stores all routing intents **in memory only**. There is no database, no WAL, no persistent state file.

**Why this works:**

1. **Clients are the source of truth.** NovaEdge knows its VIP assignments (from the controller). NovaNet knows its pod CIDRs (from the CNI config). They can re-assert intents at any time.

2. **FRR graceful restart preserves routes.** When NovaRoute restarts, FRR continues forwarding with existing routes for a configurable window (default: 120 seconds). This gives clients time to reconnect and re-assert.

3. **No stale state.** Disk persistence creates a risk of stale routes surviving client crashes. With ephemeral state, if a client disconnects without withdrawing, the intents are cleaned up after a configurable grace period (default: 30 seconds).

### Reconciliation

The reconciler runs on a 30-second ticker and can be triggered immediately by any RPC that modifies intents. On each cycle it:

1. Reads all intents from the in-memory store
2. Compares desired state against applied state (tracked in internal maps)
3. Calls vtysh to add/remove peers, prefixes, BFD sessions, and OSPF interfaces as needed
4. Updates the applied state maps

Equality checks detect changes in peer timers, prefix attributes, BFD parameters, and OSPF settings — not just additions/removals.

### Restart Sequence

```
1. NovaRoute crashes or restarts
2. FRR graceful restart activates → routes held in kernel FIB
3. NovaRoute starts, waits for FRR VTY sockets to appear
4. Clients detect broken gRPC stream, reconnect
5. Clients call Register(reassert_intents=true)
6. Clients re-send all AdvertisePrefix/ApplyPeer calls
7. NovaRoute reconciles: intents match FRR state → no-op (fast)
8. FRR graceful restart timer clears → normal operation resumes

Total disruption: 0 seconds (routes never left kernel FIB)
```

---

## Deployment Model

### Kubernetes DaemonSet

NovaRoute deploys as a DaemonSet with two containers per pod:

```yaml
containers:
  # NovaRoute agent — the routing control plane
  - name: novaroute-agent
    image: ghcr.io/piwi3910/novaroute-agent:latest
    args: ["--config=/etc/novaroute/config.json"]
    ports:
      - name: metrics
        containerPort: 9102
    volumeMounts:
      - name: run           # exposes /run/novaroute/novaroute.sock to host
        mountPath: /run/novaroute
      - name: frr-sock      # shared VTY sockets with FRR sidecar
        mountPath: /run/frr
      - name: config
        mountPath: /etc/novaroute

  # FRR sidecar — the routing engine
  - name: frr
    image: ghcr.io/piwi3910/novaroute-frr:10.5.1
    volumeMounts:
      - name: frr-sock      # FRR creates *.vty sockets here
        mountPath: /run/frr
      - name: frr-config    # bootstrap daemons + frr.conf
        mountPath: /etc/frr
```

Both containers run with `hostNetwork: true` because FRR needs to bind TCP 179 (BGP), send BFD packets, manage OSPF adjacencies, and modify the kernel FIB directly.

The `frr-sock` emptyDir volume is the bridge: FRR creates `zebra.vty`, `bgpd.vty`, etc. in `/run/frr/`, and NovaRoute's vtysh connects through those sockets.

The `/run/novaroute/novaroute.sock` is exposed to the host via a hostPath volume, so NovaEdge and other clients can connect from their own pods.

### FRR Bootstrap

FRR starts with minimal bootstrap config — NovaRoute handles all runtime configuration:

- **Enabled daemons:** bgpd, ospfd, mgmtd, zebra (watchfrr restarts crashed daemons)
- **BGP port:** `-p 0` (port 0 = don't bind TCP 179 at startup; NovaRoute configures BGP instance on demand)
- **Profile:** `datacenter` (tuned defaults for DC deployments)
- No static BGP/OSPF config in `frr.conf` — everything is applied dynamically via vtysh

---

## Configuration

NovaRoute agent reads a JSON config file (default: `/etc/novaroute/config.json`):

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
        "allowed_cidrs": ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]
      }
    }
  },
  "log_level": "info",
  "metrics_address": ":9102",
  "disconnect_grace_period": 30
}
```

### Environment Variable Support

- **Token expansion** — `${NOVAEDGE_TOKEN}` in owner tokens is expanded from environment
- **Router ID expansion** — `${NODE_IP}` in `bgp.router_id` is expanded
- **Override env vars:**
  - `NOVAROUTE_BGP_LOCAL_AS` — overrides `bgp.local_as` (used for per-node eBGP AS)
  - `NOVAROUTE_BGP_ROUTER_ID` — overrides `bgp.router_id`

### ConfigureBGP RPC

In addition to static config, clients can dynamically change the BGP AS and router ID at runtime via the `ConfigureBGP` RPC. This is how NovaEdge sets per-node AS numbers for eBGP — the controller knows each node's desired AS and pushes it via this RPC. A change triggers `no router bgp <old>` / `router bgp <new>` in FRR, tearing down and rebuilding all sessions.

---

## CLI

`novaroutectl` connects to the NovaRoute Unix socket and provides read-only status commands:

```bash
# Full status (all owners, all protocols)
novaroutectl status

# BGP peers (optionally filtered by owner)
novaroutectl peers
novaroutectl peers --owner novaedge

# Advertised prefixes
novaroutectl prefixes
novaroutectl prefixes --owner novanet

# BFD sessions
novaroutectl bfd

# OSPF interfaces
novaroutectl ospf
```

---

## Repository Structure

```
NovaRoute/
├── .github/workflows/          # CI and release pipelines
│   ├── ci.yml                  # Lint, test, build, security scan
│   └── release.yml             # Multi-arch binaries + Docker image
├── api/v1/
│   ├── novaroute.proto         # gRPC service definition (14 RPCs)
│   ├── novaroute.pb.go         # Generated protobuf code
│   └── novaroute_grpc.pb.go    # Generated gRPC stubs
├── cmd/
│   ├── novaroute-agent/        # Main agent binary
│   └── novaroutectl/           # CLI tool
├── internal/
│   ├── config/                 # JSON config loading + validation
│   ├── frr/                    # FRR vtysh client (BGP, BFD, OSPF)
│   ├── intent/                 # In-memory intent store (thread-safe)
│   ├── metrics/                # Prometheus metrics
│   ├── policy/                 # Ownership + prefix policy engine
│   ├── reconciler/             # Desired→applied state reconciliation
│   └── server/                 # gRPC handlers + event pub-sub
├── deploy/
│   ├── daemonset.yaml          # Kubernetes DaemonSet (agent + FRR sidecar)
│   └── configmap.yaml          # Agent config + FRR bootstrap
├── Dockerfile                  # Agent image (Go binary + FRR from Alpine)
├── Dockerfile.frr              # Custom FRR image (built from source)
├── Makefile                    # Build automation
├── LICENSE                     # Apache-2.0
└── README.md
```

---

## Feature Status

| Feature | Status | Notes |
|---------|--------|-------|
| **BGP peer management** | Done | Add/remove peers, timers, eBGP-multihop, password, IPv4/IPv6 AFI |
| **BGP prefix advertisement** | Done | Advertise/withdraw with attributes (local-pref, communities, MED) |
| **BGP dynamic AS/router-id** | Done | ConfigureBGP RPC for per-node eBGP AS from controller |
| **BFD sessions** | Done | Single-hop, configurable intervals and detect-multiplier |
| **OSPF interfaces** | Done | Per-interface area, cost, hello/dead timers, passive mode |
| **Intent store** | Done | In-memory, thread-safe, per-owner CRUD |
| **Policy engine** | Done | Token auth, prefix type validation (host_only/subnet/any), CIDR restrictions, conflict detection |
| **Reconciler** | Done | 30s periodic + triggered sync, desired-vs-applied diffing, equality checks |
| **Event streaming** | Done | Pub-sub with owner/type filtering, 13 event types, non-blocking delivery |
| **gRPC server** | Done | All 14 RPCs implemented, Unix socket transport |
| **CLI (novaroutectl)** | Done | Read-only status, peers, prefixes, BFD, OSPF commands |
| **Prometheus metrics** | Done | gRPC duration, policy violations, intent counts, active sessions per owner |
| **DaemonSet deployment** | Done | Agent + FRR sidecar, ConfigMaps, hostNetwork, rolling update |
| **CI/CD** | Done | Lint, test, build, security scan, multi-arch release |
| **FRR state monitoring** | Done | Reconciler queries FRR show commands (BGP/BFD/OSPF) after each cycle, detects state changes |
| **Real event publishing** | Done | Events published on FRR state transitions (peer up/down, BFD up/down, OSPF neighbor changes) |
| **Graceful shutdown** | Done | WithdrawAll removes prefixes, BFD, OSPF, peers on SIGTERM with 10s timeout |
| **Real GetStatus** | Done | GetStatus RPC returns actual FRR peer state, BFD status, OSPF state, FRR version |
| **Health check endpoints** | Done | `/healthz` always-OK liveness, `/readyz` checks FRR connectivity |
| Multi-owner peer sharing | Not yet | Two owners requesting the same peer are handled independently |
| Route-maps / BGP filters | Not yet | No route-map, community filter, or path selection policy support |
| OSPF authentication | Not yet | |
| Multi-hop BFD | Not yet | Only single-hop BFD sessions |

---

## Future Work

- **Multi-owner peer sharing** — Merge BGP sessions when multiple owners request the same peer; remove only when all owners withdraw
- **Route-maps and filters** — BGP route-map, community filter, AS-path filter support
- **NovaNet integration** — Pod CIDR advertisement with multi-owner coordination

---

## License

Apache-2.0. See [LICENSE](LICENSE) for details. FRR itself is GPL-2.0.

---

## Related Projects

- **[NovaEdge](https://github.com/piwi3910/novaedge)** — Kubernetes load balancer, reverse proxy, and SD-WAN gateway (primary consumer of NovaRoute)
- **NovaNet** — Kubernetes CNI and pod networking (future consumer)
- **[FRR](https://frrouting.org/)** — Free Range Routing, the routing engine NovaRoute controls
