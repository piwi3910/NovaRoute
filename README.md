# NovaRoute

**Node-local, policy-safe routing control plane for Kubernetes.**

NovaRoute is a unified routing control service that centralizes BGP, BFD, and OSPF management on each Kubernetes node. It acts as a single owner of the FRR (Free Range Routing) daemon, exposing a gRPC API over a Unix domain socket so that multiple clients — NovaEdge (load balancer VIPs), NovaNet (pod/node networking), and human administrators — can safely share one routing stack without conflicting.

> **Status:** Architecture & design phase. This folder will be migrated to its own repository at [github.com/piwi3910/NovaRoute](https://github.com/piwi3910/NovaRoute).

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
- [Migration Path](#migration-path)
- [MVP Phasing](#mvp-phasing)

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
│  │  │  - owner → prefix → intent     │   │          │
│  │  │  - peer sessions               │   │          │
│  │  │  - BFD sessions                │   │          │
│  │  └─────────────────────────────────┘   │          │
│  │                                         │          │
│  │  ┌─────────────────────────────────┐   │          │
│  │  │  Policy Engine                  │   │          │
│  │  │  - ownership boundaries         │   │          │
│  │  │  - prefix validation            │   │          │
│  │  │  - conflict detection           │   │          │
│  │  └─────────────────────────────────┘   │          │
│  │                                         │          │
│  │  ┌─────────────────────────────────┐   │          │
│  │  │  FRR Northbound gRPC Client     │   │          │
│  │  │  - YANG path-based config       │   │          │
│  │  │  - candidate/commit model       │   │          │
│  │  └──────────────┬──────────────────┘   │          │
│  └─────────────────┼──────────────────────┘          │
│                    │                                  │
│                    │ gRPC (localhost / shared socket)  │
│                    │                                  │
│  ┌─────────────────▼──────────────────────┐          │
│  │              FRR Daemon                 │          │
│  │  (bgpd, bfdd, ospfd, zebra, mgmtd)    │          │
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
| **NovaRoute Agent** | Accepts intents from clients via gRPC, enforces ownership policies, translates intents to FRR configuration via FRR's northbound gRPC API |
| **FRR** | Production-grade routing suite. Owns all BGP sessions (TCP 179), BFD sessions, OSPF adjacencies. Handles graceful restart, route reflection, etc. |
| **Clients** (NovaEdge, NovaNet, Admin) | Submit routing intents (advertise prefix, establish peer, enable BFD) via Unix socket gRPC. Never touch FRR directly. |

---

## Core Design Principles

### 1. Single Owner of the Routing Stack

FRR is a shared, stateful resource. NovaRoute is its sole controller. No other process on the node configures FRR directly. This eliminates conflicts, simplifies debugging, and enables policy enforcement.

### 2. Intent-Based, Not Imperative

Clients declare **what** they want ("advertise 10.0.0.1/32 via BGP"), not **how** to achieve it. NovaRoute translates intents into FRR configuration. This decouples clients from FRR internals and allows NovaRoute to optimize (e.g., batching multiple prefix changes into a single FRR commit).

### 3. Policy-Safe by Default

Every intent is validated against ownership rules before reaching FRR:
- NovaEdge can only advertise /32 VIP addresses
- NovaNet can only advertise pod/node CIDR subnets
- Overlap between owners is rejected
- Admin can override with explicit grants

### 4. Ephemeral State, Durable Routing

NovaRoute stores intents in memory only. On restart, clients re-assert their intents (they already have the source of truth — e.g., NovaEdge knows its VIP assignments). FRR's graceful restart holds existing routes in the kernel during the gap, ensuring zero traffic disruption.

### 5. Observable

A single `novaroutectl status` command shows everything the node is advertising, all peer sessions, BFD status, and which client owns each route. No more guessing across multiple processes.

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
| Programmatic API | Northbound gRPC (YANG) | gRPC | BIRD socket |
| License | GPL-2.0 | Apache-2.0 | GPL-2.0 |

FRR is the only option that provides BGP + BFD + OSPF in a single daemon with a programmatic gRPC API. It's the industry standard for software-defined routing on Linux.

### FRR Northbound gRPC Interface

NovaRoute controls FRR via its **northbound gRPC API**, which provides YANG-modeled configuration with a transactional candidate/commit model.

#### Why Northbound gRPC (vs. vtysh, FPM, etc.)

| Interface | Latency | Transactional | Structured | Use Case |
|-----------|---------|---------------|-----------|----------|
| **Northbound gRPC** | ~1-5 ms | Yes (candidate/commit) | YANG paths | Programmatic control |
| vtysh CLI | ~50 ms | No | Text parsing | Human operators |
| FPM (Forwarding Plane Manager) | ~1 ms | No | Netlink | Route mirroring only |
| SNMP | ~10 ms | No | MIB OIDs | Monitoring only |

Northbound gRPC is the fastest programmatic interface with full transactional semantics. It uses YANG data paths for structured configuration, avoiding fragile text parsing.

#### Transaction Model

```
CreateCandidate(name)     →  candidate_id
EditCandidate(id, path, value)  →  (repeated for each change)
Commit(id, phase=ALL)     →  applied atomically
DeleteCandidate(id)       →  cleanup
```

Multiple prefix advertisements and peer configurations are batched into a single candidate and committed atomically. This ensures FRR never sees a partial configuration.

#### YANG Path Mappings

NovaRoute translates client intents into FRR YANG paths:

**BGP Peer Configuration:**
```
/frr-routing:routing/control-plane-protocols/control-plane-protocol
  [type='frr-bgp:bgp'][name='default']/frr-bgp:bgp/neighbors/neighbor[remote-address='{IP}']
    /neighbor-remote-as/remote-as-type = external|internal
    /enabled = true
    /timers/hold-time = 9
    /timers/keepalive = 3
```

**BGP Network Advertisement:**
```
/frr-routing:routing/control-plane-protocols/control-plane-protocol
  [type='frr-bgp:bgp'][name='default']/frr-bgp:bgp/global/afi-safis/afi-safi
  [afi-safi-name='frr-routing:ipv4-unicast']/network-config[prefix='{PREFIX}']
```

**BFD Session:**
```
/frr-bfdd:bfdd/bfd/sessions/single-hop[dest-addr='{IP}']
    /required-min-rx = 300
    /desired-min-tx = 300
    /detection-multiplier = 3
```

**OSPF Interface:**
```
/frr-routing:routing/control-plane-protocols/control-plane-protocol
  [type='frr-ospfd:ospf'][name='default']/frr-ospfd:ospf/areas/area[area-id='{AREA}']
    /interfaces/interface[name='{IFACE}']/enabled = true
```

---

## API Surface

NovaRoute exposes a gRPC API over a Unix domain socket at `/run/novaroute/novaroute.sock`.

### Service Definition (Conceptual)

```protobuf
syntax = "proto3";
package novaroute.v1;

service RouteControl {
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

  // Session management
  rpc Register(RegisterRequest) returns (RegisterResponse);
}

// --- Peer Management ---

message ApplyPeerRequest {
  string owner = 1;            // "novaedge", "novanet", "admin"
  string token = 2;            // ownership verification
  BGPPeer peer = 3;
}

message BGPPeer {
  string neighbor_address = 1; // e.g., "192.168.1.1"
  uint32 remote_as = 2;        // e.g., 65000
  string peer_type = 3;        // "external" or "internal"
  uint32 keepalive = 4;        // seconds (default: 3)
  uint32 hold_time = 5;        // seconds (default: 9)
  bool bfd_enabled = 6;        // enable BFD for this peer
  string description = 7;
}

// --- Prefix Advertisement ---

message AdvertisePrefixRequest {
  string owner = 1;
  string token = 2;
  string prefix = 3;           // e.g., "10.0.0.1/32"
  string protocol = 4;         // "bgp" or "ospf"
  map<string, string> attributes = 5; // communities, local-pref, etc.
}

message WithdrawPrefixRequest {
  string owner = 1;
  string token = 2;
  string prefix = 3;
  string protocol = 4;
}

// --- BFD ---

message EnableBFDRequest {
  string owner = 1;
  string token = 2;
  string peer_address = 3;
  uint32 min_rx = 4;           // microseconds (default: 300000)
  uint32 min_tx = 5;           // microseconds (default: 300000)
  uint32 detect_multiplier = 6; // default: 3
}

// --- OSPF ---

message EnableOSPFRequest {
  string owner = 1;
  string token = 2;
  string interface_name = 3;
  string area_id = 4;          // e.g., "0.0.0.0"
  bool passive = 5;            // passive interface (advertise but don't form adjacency)
}

// --- Observability ---

message GetStatusResponse {
  repeated PeerStatus peers = 1;
  repeated PrefixStatus prefixes = 2;
  repeated BFDStatus bfd_sessions = 3;
  repeated OSPFStatus ospf_interfaces = 4;
}

message PeerStatus {
  string neighbor_address = 1;
  uint32 remote_as = 2;
  string state = 3;            // "Established", "Connect", "Active", etc.
  string owner = 4;
  uint32 prefixes_received = 5;
  uint32 prefixes_sent = 6;
  string uptime = 7;
}

message PrefixStatus {
  string prefix = 1;
  string protocol = 2;
  string owner = 3;
  string state = 4;            // "advertised", "pending", "withdrawn"
}

message RouteEvent {
  string type = 1;             // "peer_up", "peer_down", "bfd_down", "prefix_added", etc.
  string detail = 2;
  int64 timestamp = 3;
  map<string, string> metadata = 4;
}

// --- Session Management ---

message RegisterRequest {
  string owner = 1;            // client identity
  string token = 2;            // pre-shared token for ownership verification
  bool reassert_intents = 3;   // true = client will re-send all current intents
}

message RegisterResponse {
  string session_id = 1;
  repeated string current_prefixes = 2;  // what we currently hold for this owner
  repeated string current_peers = 3;
}
```

### Key API Behaviors

1. **Idempotent operations** — calling `AdvertisePrefix` for an already-advertised prefix is a no-op
2. **Owner scoping** — all mutations require an owner field; withdrawing another owner's prefix is rejected
3. **Batch-friendly** — NovaRoute internally batches rapid successive calls into a single FRR commit
4. **Event streaming** — `StreamEvents` provides real-time peer state changes, BFD notifications, etc.
5. **Session registration** — on (re)connect, clients call `Register` to get their current state and optionally re-assert all intents

---

## Ownership Model

The ownership model is the core safety mechanism. It prevents clients from interfering with each other's routes.

### Ownership Boundaries

| Owner | Allowed Prefixes | Allowed Protocols | Restrictions |
|-------|-----------------|-------------------|-------------|
| **novaedge** | /32 host routes only (VIP addresses) | BGP, OSPF | Cannot advertise subnets |
| **novanet** | Subnet prefixes (/8 - /28) for pod CIDRs and node CIDRs | BGP | Cannot advertise /32 host routes |
| **admin** | Any prefix | Any | Explicit grants, highest priority |

### Conflict Resolution

1. **Same prefix, same owner** — idempotent update (latest attributes win)
2. **Same prefix, different owners** — **rejected** with error. Admin must explicitly grant override.
3. **Peer overlap** — two owners can request the same BGP peer; NovaRoute merges the session (union of address families). Peer is removed only when all owners withdraw it.
4. **Admin override** — admin-owned routes take priority. If admin advertises a prefix that conflicts with novaedge/novanet, the conflicting client's intent is held (not dropped) and re-applied if admin withdraws.

### Ownership Verification

Each client authenticates with a pre-shared token configured at NovaRoute startup:

```yaml
# /etc/novaroute/config.yaml
owners:
  novaedge:
    token: "${NOVAEDGE_TOKEN}"
    allowed_prefixes:
      - type: host_only  # /32 only
  novanet:
    token: "${NOVANET_TOKEN}"
    allowed_prefixes:
      - type: subnet     # /8 - /28
  admin:
    token: "${ADMIN_TOKEN}"
    allowed_prefixes:
      - type: any
```

---

## State Model

### Intent-Based, No Disk Persistence

NovaRoute stores all routing intents **in memory only**. There is no database, no WAL, no persistent state file.

**Why this works:**

1. **Clients are the source of truth.** NovaEdge knows its VIP assignments (from the controller). NovaNet knows its pod CIDRs (from the CNI config). They can re-assert intents at any time.

2. **FRR graceful restart preserves routes.** When NovaRoute restarts, FRR continues forwarding with existing routes for a configurable window (default: 120 seconds). This gives clients time to reconnect and re-assert.

3. **No stale state.** Disk persistence creates a risk of stale routes surviving client crashes. With ephemeral state, if a client disconnects without withdrawing, NovaRoute can detect the broken session and optionally clean up (configurable: immediate cleanup vs. grace period).

### Restart Sequence

```
1. NovaRoute crashes or restarts
2. FRR graceful restart activates → routes held in kernel FIB
3. NovaRoute starts, connects to FRR via northbound gRPC
4. Clients detect broken gRPC stream, reconnect
5. Clients call Register(reassert_intents=true)
6. Clients re-send all AdvertisePrefix/ApplyPeer calls
7. NovaRoute reconciles: intents match FRR state → no-op (fast)
8. FRR graceful restart timer clears → normal operation resumes

Total disruption: 0 seconds (routes never left kernel FIB)
```

### Client Disconnect Handling

| Scenario | Behavior |
|----------|----------|
| Client disconnects cleanly (calls `Deregister`) | All intents for that owner are withdrawn immediately |
| Client crashes (gRPC stream breaks) | Grace period (default: 30s), then withdraw all intents for that owner |
| Client reconnects within grace period | Intents preserved, client re-asserts to confirm |
| NovaRoute restarts | FRR holds routes; clients reconnect and re-assert |

---

## Deployment Model

### Kubernetes DaemonSet

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: novaroute-agent
  namespace: novaroute-system
spec:
  selector:
    matchLabels:
      app: novaroute-agent
  template:
    metadata:
      labels:
        app: novaroute-agent
    spec:
      hostNetwork: true
      containers:
        # NovaRoute agent — the control plane
        - name: novaroute-agent
          image: ghcr.io/piwi3910/novaroute-agent:latest
          volumeMounts:
            - name: run
              mountPath: /run/novaroute
            - name: frr-sock
              mountPath: /run/frr
            - name: config
              mountPath: /etc/novaroute
          securityContext:
            capabilities:
              add: [NET_ADMIN]

        # FRR sidecar — the routing engine
        - name: frr
          image: quay.io/frrouting/frr:10.3.1
          volumeMounts:
            - name: frr-sock
              mountPath: /run/frr
            - name: frr-config
              mountPath: /etc/frr
          securityContext:
            capabilities:
              add: [NET_ADMIN, NET_RAW, SYS_ADMIN]

      volumes:
        - name: run
          hostPath:
            path: /run/novaroute
            type: DirectoryOrCreate
        - name: frr-sock
          emptyDir: {}
        - name: config
          configMap:
            name: novaroute-config
        - name: frr-config
          configMap:
            name: novaroute-frr-bootstrap
```

### Why `hostNetwork: true`

FRR needs to:
- Bind TCP 179 (BGP) on the host's IP
- Send/receive BFD packets on host interfaces
- Manage OSPF adjacencies on host interfaces
- Directly modify the kernel FIB (routing table)

### Shared Socket Volume

The `frr-sock` volume (emptyDir) is shared between the NovaRoute agent and FRR containers. NovaRoute connects to FRR's northbound gRPC socket at `/run/frr/mgmtd_fe.sock` (or the configured address).

The `run` volume exposes `/run/novaroute/novaroute.sock` to the host, where NovaEdge and NovaNet agents connect.

---

## Migration Path

### From GoBGP (NovaEdge) to NovaRoute

NovaEdge currently embeds GoBGP for VIP announcement. The migration path:

#### Phase 1: Dual-Stack (NovaRoute + GoBGP coexist)

```
NovaEdge Agent
  ├── VIP Manager
  │   ├── GoBGP (existing, default)
  │   └── NovaRoute client (opt-in via flag)
  └── Config: bgp_backend: "gobgp" | "novaroute"
```

- NovaEdge gains a NovaRoute client alongside GoBGP
- Operators opt-in per cluster with a configuration flag
- Both paths are tested; NovaRoute path validates end-to-end

#### Phase 2: NovaRoute Default

- Default flips to `novaroute`
- GoBGP code path remains as fallback
- Documentation updated

#### Phase 3: GoBGP Removal

- GoBGP dependency removed from NovaEdge
- NovaRoute becomes the only BGP path
- `internal/agent/vip/bgp.go` → simplified to NovaRoute gRPC calls

### Code Changes in NovaEdge

The VIP manager's BGP integration (`internal/agent/vip/`) changes from:

```go
// Before: Direct GoBGP embedding
type BGPManager struct {
    server *gobgp.BgpServer
    // ... complex GoBGP lifecycle management
}

func (m *BGPManager) AdvertiseVIP(vip net.IP) error {
    // 50+ lines of GoBGP path construction
}
```

To:

```go
// After: NovaRoute client call
type BGPManager struct {
    client novaroutepb.RouteControlClient
}

func (m *BGPManager) AdvertiseVIP(vip net.IP) error {
    _, err := m.client.AdvertisePrefix(ctx, &novaroutepb.AdvertisePrefixRequest{
        Owner:    "novaedge",
        Token:    m.token,
        Prefix:   vip.String() + "/32",
        Protocol: "bgp",
    })
    return err
}
```

---

## MVP Phasing

### MVP 1: BGP + BFD Core

**Goal:** Replace GoBGP in NovaEdge with NovaRoute for VIP advertisement.

**Scope:**
- NovaRoute agent binary with gRPC server (Unix socket)
- FRR northbound gRPC client
- BGP peer management (ApplyPeer, RemovePeer)
- BGP prefix advertisement (AdvertisePrefix, WithdrawPrefix)
- BFD session management (EnableBFD, DisableBFD)
- Ownership model with token authentication
- Intent store (in-memory)
- Basic status API (GetStatus)
- DaemonSet deployment with FRR sidecar
- `novaroutectl` CLI for debugging

**Not in MVP 1:** OSPF, event streaming, admin overrides, NovaNet integration.

### MVP 2: OSPF + Events

**Scope:**
- OSPF interface management (EnableOSPF, DisableOSPF)
- Event streaming (StreamEvents)
- Client disconnect detection and grace period cleanup
- Admin override/grant mechanism
- Prometheus metrics endpoint

### MVP 3: NovaNet Integration

**Scope:**
- NovaNet client integration for pod CIDR advertisement
- Multi-owner peer sharing (NovaEdge + NovaNet share BGP peer)
- Full conflict detection and resolution
- Route policy / route-map management
- BGP community and local-pref support

---

## Repository Structure (Planned)

```
NovaRoute/
├── cmd/
│   ├── novaroute-agent/        # Main agent binary
│   └── novaroutectl/           # CLI debugging tool
├── api/
│   └── v1/                     # Protobuf definitions
│       └── novaroute.proto
├── internal/
│   ├── server/                 # gRPC server (Unix socket)
│   ├── intent/                 # Intent store (in-memory)
│   ├── policy/                 # Ownership & prefix validation
│   ├── frr/                    # FRR northbound gRPC client
│   │   ├── client.go           # Connection management
│   │   ├── bgp.go              # BGP YANG path translations
│   │   ├── bfd.go              # BFD YANG path translations
│   │   └── ospf.go             # OSPF YANG path translations
│   ├── reconciler/             # Intent → FRR state reconciliation
│   └── metrics/                # Prometheus metrics
├── deploy/
│   ├── daemonset.yaml          # Kubernetes DaemonSet
│   ├── configmap.yaml          # NovaRoute + FRR bootstrap config
│   └── helm/                   # Helm chart
├── docs/
│   ├── architecture.md
│   ├── api-reference.md
│   └── migration-guide.md
├── go.mod
├── go.sum
├── Makefile
├── Dockerfile
└── README.md
```

---

## License

TBD (likely Apache-2.0 for the NovaRoute agent; FRR itself is GPL-2.0).

---

## Related Projects

- **[NovaEdge](https://github.com/piwi3910/novaedge)** — Kubernetes load balancer, reverse proxy, and SD-WAN gateway (primary consumer of NovaRoute)
- **NovaNet** — Kubernetes CNI and pod networking (future consumer)
- **[FRR](https://frrouting.org/)** — Free Range Routing, the routing engine NovaRoute controls
