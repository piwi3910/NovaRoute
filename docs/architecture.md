# Architecture

NovaRoute is designed as a single point of control for all routing protocols on a Kubernetes node. This page explains the system architecture, component roles, data flow, package structure, and key design decisions.

---

## Architecture Diagram

```
+---------------------------------------------------------+
|                   Kubernetes Node                        |
|                                                          |
|  +----------+  +----------+  +----------+               |
|  | NovaEdge |  | NovaNet  |  |  Admin   |               |
|  |  Agent   |  |  Agent   |  |  (CLI)   |               |
|  +----+-----+  +----+-----+  +----+-----+               |
|       |              |              |                     |
|       |    Unix Socket gRPC         |                     |
|       |   /run/novaroute/novaroute.sock                  |
|       |              |              |                     |
|  +----v--------------v--------------v-----+              |
|  |            NovaRoute Agent              |              |
|  |                                         |              |
|  |  +-------------------------------------+   |          |
|  |  |  Intent Store (in-memory)           |   |          |
|  |  |  - owner -> peers/prefixes          |   |          |
|  |  |  - BFD/OSPF sessions               |   |          |
|  |  +-------------------------------------+   |          |
|  |                                         |              |
|  |  +-------------------------------------+   |          |
|  |  |  Policy Engine                      |   |          |
|  |  |  - token authentication             |   |          |
|  |  |  - prefix type validation           |   |          |
|  |  |  - cross-owner conflict check       |   |          |
|  |  +-------------------------------------+   |          |
|  |                                         |              |
|  |  +-------------------------------------+   |          |
|  |  |  Reconciler                         |   |          |
|  |  |  - desired vs applied diffing       |   |          |
|  |  |  - periodic + triggered sync        |   |          |
|  |  +-----------------+-------------------+   |          |
|  |                    |                       |          |
|  |  +-----------------v-------------------+   |          |
|  |  |  FRR Client (vtysh CLI)             |   |          |
|  |  |  - configure terminal batches       |   |          |
|  |  |  - show commands for status         |   |          |
|  |  +-----------------+-------------------+   |          |
|  +---------------------+---------------------+          |
|                        |                                  |
|                        | vtysh over VTY Unix sockets      |
|                        | (/run/frr/zebra.vty, bgpd.vty)  |
|                        |                                  |
|  +---------------------v---------------------+           |
|  |              FRR Daemon                     |          |
|  |  (bgpd, bfdd, ospfd, zebra, mgmtd)        |          |
|  |                                             |          |
|  |  TCP 179 --- BGP sessions --- Routers      |          |
|  |  BFD ------- Link detection ------ ^       |          |
|  |  OSPF ------ Area adjacencies ---- ^       |          |
|  +---------------------------------------------+         |
+---------------------------------------------------------+
```

---

## Component Descriptions

### gRPC Server

The gRPC server listens on a Unix domain socket at `/run/novaroute/novaroute.sock` and implements the `RouteControl` service with 13 RPCs. It handles:

- **Session management** -- `Register` and `Deregister` for owner lifecycle
- **BGP configuration** -- `ConfigureBGP` for dynamic AS/router-id changes at runtime
- **Peer management** -- `ApplyPeer` and `RemovePeer` for BGP neighbor setup
- **Prefix advertisement** -- `AdvertisePrefix` and `WithdrawPrefix` for route announcements
- **BFD** -- `EnableBFD` and `DisableBFD` for bidirectional forwarding detection sessions
- **OSPF** -- `EnableOSPF` and `DisableOSPF` for per-interface OSPF configuration
- **Observability** -- `GetStatus` for point-in-time state queries and `StreamEvents` for real-time event streaming

Every mutating RPC follows the same flow: authenticate the owner token, validate the request against the policy engine, store the intent, and trigger the reconciler.

The server also hosts an event bus (pub-sub) that delivers events to all active `StreamEvents` subscribers with per-owner and per-type filtering and per-subscriber buffered channels for non-blocking delivery.

### Intent Store

The intent store is a thread-safe, in-memory data structure that holds the desired routing state grouped by owner. It stores:

- BGP peers (neighbor address, remote AS, timers, address families, etc.)
- Advertised prefixes (CIDR, protocol, attributes like local-pref, communities, MED)
- BFD sessions (peer address, intervals, detect multiplier)
- OSPF interfaces (interface name, area, cost, timers, passive mode)

The store is intentionally ephemeral -- there is no persistence to disk. On agent restart, clients reconnect and re-assert their intents. This design avoids stale state and ensures clients remain the source of truth.

### Policy Engine

The policy engine validates every intent before it reaches the store. It enforces:

1. **Token authentication** -- Each owner must present a pre-shared token that matches the configured value
2. **Prefix type validation** -- Owners configured as `host_only` can only advertise /32 (IPv4) and /128 (IPv6) routes; `subnet` owners can only advertise /8 through /28 ranges; `any` owners have no restrictions
3. **CIDR restrictions** -- If `allowed_cidrs` is configured for an owner, every advertised prefix must fall within at least one of the allowed CIDR ranges
4. **Cross-owner conflict detection** -- If two different owners try to advertise the same prefix, the request is rejected (unless the requesting owner is `admin`, which can override)

Policy violations are published as events (`EVENT_TYPE_POLICY_VIOLATION`) and tracked via Prometheus metrics.

### Reconciler

The reconciler bridges the gap between desired state (intents) and applied state (what FRR is actually configured with). It runs in two modes:

- **Periodic** -- A 30-second ticker triggers a full reconciliation cycle
- **Triggered** -- Any RPC that modifies intents (ApplyPeer, AdvertisePrefix, etc.) immediately triggers a reconciliation

On each cycle, the reconciler:

1. Reads all intents from the in-memory store
2. Compares desired state against the applied state (tracked in internal maps)
3. Calls the FRR client to add, remove, or update peers, prefixes, BFD sessions, and OSPF interfaces
4. Updates the applied state maps to reflect what was successfully configured
5. Queries FRR show commands to read actual state (peer status, BFD status, OSPF neighbors)
6. Detects state changes (peer up/down, BFD transitions) and publishes events

Equality checks go beyond simple add/remove detection -- the reconciler detects changes in peer timers, prefix attributes, BFD parameters, and OSPF settings, triggering updates when any field differs.

### FRR Client

The FRR client executes vtysh commands against the local FRR daemon. It communicates via VTY Unix sockets located in `/run/frr/` (e.g., `zebra.vty`, `bgpd.vty`).

Two types of operations:

| Type | Method | Example |
|------|--------|---------|
| **Show commands** | `vtysh --vty_socket /run/frr -c "show ..."` | `show bgp summary json`, `show bfd peers json` |
| **Configuration batches** | `vtysh --vty_socket /run/frr -f /tmp/batch.conf` | `configure terminal` / `router bgp 65011` / `neighbor ...` / `end` |

Configuration batches are written to temporary files and applied atomically by vtysh. If a command fails, the intent remains in the desired state and will be retried on the next reconciliation cycle.

---

## Data Flow

A typical `AdvertisePrefix` request flows through the system as follows:

```
Client                 gRPC Server          Policy Engine        Intent Store         Reconciler           FRR Client
  |                        |                     |                    |                    |                    |
  |-- AdvertisePrefix ---->|                     |                    |                    |                    |
  |                        |-- validate token -->|                    |                    |                    |
  |                        |-- check prefix   -->|                    |                    |                    |
  |                        |-- check conflicts-->|                    |                    |                    |
  |                        |<-- OK --------------|                    |                    |                    |
  |                        |-- store intent ---->|                    |                    |                    |
  |                        |                     |<-- stored ---------|                    |                    |
  |                        |-- trigger --------->|                    |-- reconcile ------>|                    |
  |                        |                     |                    |                    |-- vtysh command -->|
  |                        |                     |                    |                    |<-- success --------|
  |                        |                     |                    |-- update applied ->|                    |
  |<-- OK -----------------|                     |                    |                    |                    |
```

---

## Package Structure

The codebase follows Go's standard project layout with internal packages:

| Package | Path | Responsibility |
|---------|------|----------------|
| **config** | `internal/config/` | JSON config file loading, validation, environment variable expansion, and default values |
| **frr** | `internal/frr/` | FRR vtysh client -- executes show commands and configuration batches over VTY Unix sockets |
| **intent** | `internal/intent/` | Thread-safe in-memory intent store with per-owner CRUD operations for peers, prefixes, BFD, and OSPF |
| **metrics** | `internal/metrics/` | Prometheus metric definitions and registration -- gRPC call duration, policy violations, intent counts, active sessions |
| **policy** | `internal/policy/` | Ownership and prefix policy engine -- token auth, prefix type validation, CIDR restrictions, conflict detection |
| **reconciler** | `internal/reconciler/` | Desired-to-applied state reconciliation -- periodic and triggered sync, equality checks, FRR state monitoring, event publishing |
| **server** | `internal/server/` | gRPC service handlers for all 13 RPCs, event bus (pub-sub) for StreamEvents, and HTTP health/metrics endpoints |
| **operator** | `internal/operator/` | Kubernetes operator reconciler for CRD-based routing configuration |

Additional top-level directories:

| Directory | Contents |
|-----------|----------|
| `api/v1/` | Protobuf service definition (`novaroute.proto`) and generated Go code |
| `api/v1alpha1/` | CRD API types for the Kubernetes operator |
| `cmd/novaroute-agent/` | Main entry point for the agent daemon |
| `cmd/novaroute-operator/` | Main entry point for the Kubernetes operator |
| `cmd/novaroute-test/` | Integration test binary |
| `cmd/novaroutectl/` | CLI tool for inspecting and controlling the agent |
| `config/` | Kubernetes CRD and RBAC manifests |
| `charts/` | Helm charts for deployment |
| `deploy/` | Kubernetes manifests -- DaemonSet (`daemonset.yaml`) and ConfigMap (`configmap.yaml`) |

---

## Design Principles

### 1. Single Owner of the Routing Stack

FRR is a shared, stateful resource. NovaRoute is its sole controller. No other process on the node configures FRR directly. This eliminates conflicts between components, simplifies debugging (one place to inspect all routing state), and enables centralized policy enforcement.

### 2. Intent-Based, Not Imperative

Clients declare **what** they want ("advertise 10.0.0.1/32 via BGP"), not **how** to achieve it. NovaRoute translates intents into FRR vtysh commands. This decouples clients from FRR internals and allows the reconciler to handle retries, ordering, and state diffing transparently.

### 3. Policy-Safe by Default

Every intent is validated against ownership rules before reaching FRR. NovaEdge can only advertise /32 VIP addresses. NovaNet can only advertise pod/node CIDR subnets. Overlap between owners is rejected. This is enforced at the API layer, not through convention.

### 4. Ephemeral State, Durable Routing

NovaRoute stores intents in memory only. On restart, clients re-assert their intents (they already have the source of truth -- NovaEdge knows its VIP assignments, NovaNet knows its pod CIDRs). FRR's graceful restart holds existing routes in the kernel FIB during the gap, ensuring zero traffic disruption.

### 5. Observable

A single `novaroutectl status` command shows everything the node is advertising, all peer sessions, BFD status, OSPF state, and which client owns each route. Prometheus metrics, health endpoints, and real-time event streaming provide additional observability layers.

---

## FRR Integration Details

### Why FRR?

| Criteria | FRR | GoBGP | BIRD |
|----------|-----|-------|------|
| BGP | Full (iBGP, eBGP, ECMP, communities, route maps) | Good | Full |
| BFD | Native (bfdd) | None | None |
| OSPF | Native (ospfd/ospf6d) | None | Full |
| Graceful Restart | Full | Partial | Full |
| Production track record | Massive (datacenters, ISPs) | Moderate | Good |

FRR is the only option that provides BGP + BFD + OSPF in a single daemon suite. It is the industry standard for software-defined routing on Linux.

### How vtysh Works

Each FRR daemon (zebra, bgpd, ospfd, mgmtd) creates a VTY Unix socket in `/run/frr/` when it starts. The `vtysh` unified shell connects to these sockets to execute commands.

NovaRoute invokes vtysh in two ways:

**Show commands** -- for reading state:
```bash
vtysh --vty_socket /run/frr -c "show bgp summary json"
vtysh --vty_socket /run/frr -c "show bfd peers json"
vtysh --vty_socket /run/frr -c "show ip ospf neighbor json"
```

**Configuration batches** -- for applying changes:
```bash
vtysh --vty_socket /run/frr -f /tmp/batch.conf
```

Where `batch.conf` contains a sequence of commands like:

```
configure terminal
router bgp 65011
  neighbor 192.168.100.1 remote-as 65000
  address-family ipv4 unicast
    network 192.168.100.10/32
  exit-address-family
end
```

### Why vtysh Instead of FRR's Northbound gRPC?

| Approach | Pros | Cons |
|----------|------|------|
| **vtysh CLI** (current) | No extra dependencies, works with stock FRR, simple to debug, reliable | Text output parsing, no transactional candidate/commit |
| FRR northbound gRPC | YANG-modeled, transactional commits | Requires mgmtd with gRPC compiled, complex protobuf, immature API surface |

NovaRoute chose vtysh for reliability and simplicity. The reconciler's desired-vs-applied diffing provides equivalent consistency guarantees -- if a vtysh command fails, the intent remains in the desired state and will be retried on the next reconciliation cycle.

### VTY Sockets

NovaRoute checks for FRR readiness by looking for these sockets in the configured `frr.socket_dir` (default: `/run/frr/`):

| Socket | Daemon | Purpose |
|--------|--------|---------|
| `zebra.vty` | zebra | Kernel FIB management, interface state |
| `bgpd.vty` | bgpd | BGP session and route management |
| `ospfd.vty` | ospfd | OSPF adjacency management |
| `bfdd.vty` | bfdd | BFD session management |
| `mgmtd.vty` | mgmtd | Management daemon for FRR northbound interface |

The `/readyz` health endpoint returns HTTP 200 only when the required VTY sockets are present and accessible.

---

## Reconciliation Loop Details

### Timing

The reconciler operates on two triggers:

1. **Periodic ticker** -- Every 30 seconds, a full reconciliation cycle runs regardless of whether any intents have changed. This catches any drift between desired and applied state (e.g., if an FRR command failed silently).

2. **Immediate trigger** -- Any gRPC RPC that modifies the intent store (ApplyPeer, RemovePeer, AdvertisePrefix, WithdrawPrefix, EnableBFD, DisableBFD, EnableOSPF, DisableOSPF, ConfigureBGP) triggers an immediate reconciliation after storing the intent.

### Desired vs. Applied State

The reconciler maintains two views:

- **Desired state** -- read from the intent store (what clients have declared)
- **Applied state** -- tracked in internal maps (what has been successfully configured in FRR)

On each cycle, it computes the diff:

| Condition | Action |
|-----------|--------|
| Intent exists in desired but not in applied | Add to FRR via vtysh |
| Intent exists in applied but not in desired | Remove from FRR via vtysh |
| Intent exists in both but fields differ | Update in FRR via vtysh |
| Intent exists in both and fields match | No-op |

### Equality Checks

The reconciler does not just compare presence -- it compares full object equality. For example, if a BGP peer's keepalive timer changes from 30 to 60, the reconciler detects this and reconfigures the peer in FRR. This applies to:

- Peer timers, address families, eBGP multihop, password, source address, max-prefix
- Prefix attributes (local-preference, communities, MED, next-hop)
- BFD intervals and detect multiplier
- OSPF cost, hello/dead intervals, passive mode

### FRR State Monitoring

After each reconciliation cycle, the reconciler queries FRR show commands to read the actual state:

- `show bgp summary json` -- peer states (Idle, Connect, Active, OpenSent, OpenConfirm, Established)
- `show bfd peers json` -- BFD session states (up, down, init)
- `show ip ospf neighbor json` -- OSPF neighbor states

State changes are detected by comparing current FRR state with the previously observed state. When a change is detected (e.g., a peer transitions from Active to Established), the reconciler publishes an event through the event bus.

---

## Restart Sequence

NovaRoute is designed for zero-disruption restarts through FRR's graceful restart mechanism:

```
1. NovaRoute crashes or restarts
2. FRR graceful restart activates -> routes held in kernel FIB
3. NovaRoute starts, waits for FRR VTY sockets to appear
4. Clients detect broken gRPC stream, reconnect
5. Clients call Register(reassert_intents=true)
6. Clients re-send all AdvertisePrefix / ApplyPeer calls
7. NovaRoute reconciles: intents match FRR state -> no-op (fast)
8. FRR graceful restart timer clears -> normal operation resumes

Total disruption: 0 seconds (routes never left kernel FIB)
```

During the restart window (default: 120 seconds for FRR graceful restart), the kernel FIB retains all routes that were previously installed by FRR. Traffic continues to flow normally. Once NovaRoute restarts and clients reconnect, the reconciler finds that the desired state matches what FRR already has configured, resulting in a fast no-op reconciliation.

### Graceful Shutdown

On SIGTERM, NovaRoute performs a graceful shutdown sequence with a 10-second timeout:

1. Publishes an FRR disconnected event
2. Cancels the main context
3. Waits for the reconciler to stop
4. Calls WithdrawAll() to remove all peers, prefixes, BFD sessions, and OSPF interfaces from FRR
5. Stops the gRPC server
6. Stops the metrics server
7. Closes the FRR client
8. Removes the Unix socket
