---
layout: default
title: API Reference
nav_order: 5
---

# gRPC API Reference
{: .no_toc }

NovaRoute exposes a single gRPC service over a Unix domain socket at `/run/novaroute/novaroute.sock`. All clients -- NovaEdge, NovaNet, admin tools -- use this API to declare routing intents.

<details open markdown="block">
  <summary>Table of contents</summary>
  {: .text-delta }
- TOC
{:toc}
</details>

---

## Service: RouteControl

**Package:** `novaroute.v1`

**Transport:** gRPC over Unix domain socket

**Proto file:** `api/v1/novaroute.proto`

### Key behaviors

- **Owner scoping** -- Every mutation is scoped to an `owner` identity. Owners can only see and modify their own intents.
- **Token authentication** -- Every mutating RPC requires a valid `token` that matches the owner's configured token.
- **Idempotent operations** -- Calling `ApplyPeer` or `AdvertisePrefix` with the same parameters is safe to repeat. The agent stores the latest intent and reconciles accordingly.
- **Policy enforcement** -- Prefix advertisements are validated against the owner's configured prefix policy (`host_only`, `subnet`, or `any`) before the intent is stored.
- **Validation rules** -- `hold_time` must be at least 3 times `keepalive`. `dead_interval` must be at least `hello_interval`.

---

## Session Management

### Register

Registers an owner session with the agent. Must be called before any other mutating RPCs for that owner.

**Request: `RegisterRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity name (e.g. `novaedge`, `novanet`). **Required.** |
| `token` | `string` | Authentication token for this owner. **Required.** |
| `reassert_intents` | `bool` | If `true`, re-push all previously stored intents to FRR. Useful after agent restart. |

**Response: `RegisterResponse`**

| Field | Type | Description |
|:------|:-----|:------------|
| `session_id` | `string` | Unique session identifier for this registration. |
| `current_prefixes` | `repeated string` | List of prefixes currently advertised by this owner. |
| `current_peers` | `repeated string` | List of BGP peer addresses currently configured by this owner. |

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Token does not match the configured token for this owner, or owner name is not present in the agent configuration. |
| `ALREADY_EXISTS` | Owner is already registered with an active session. |

**Example:**

```go
resp, err := client.Register(ctx, &v1.RegisterRequest{
    Owner:           "novaedge",
    Token:           "novaedge-test-token-2024",
    ReassertIntents: true,
})
fmt.Printf("session=%s peers=%v prefixes=%v\n",
    resp.SessionId, resp.CurrentPeers, resp.CurrentPrefixes)
```

---

### Deregister

Deregisters an owner session. Optionally withdraws all routing intents.

**Request: `DeregisterRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity name. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `withdraw_all` | `bool` | If `true`, remove all peers and withdraw all prefixes owned by this owner from FRR. |

**Response: `DeregisterResponse`**

Empty message. Success is indicated by a non-error response.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `FAILED_PRECONDITION` | Owner is not currently registered. |

**Example:**

```go
_, err := client.Deregister(ctx, &v1.DeregisterRequest{
    Owner:       "novaedge",
    Token:       "novaedge-test-token-2024",
    WithdrawAll: true,
})
```

---

## BGP Configuration

### ConfigureBGP

Sets the global BGP autonomous system number and router ID. This configures the BGP instance in FRR.

**Request: `ConfigureBGPRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `local_as` | `uint32` | Local autonomous system number. **Required.** |
| `router_id` | `string` | BGP router ID in dotted-quad format (e.g. `10.0.0.1`). **Required.** |

**Response: `ConfigureBGPResponse`**

| Field | Type | Description |
|:------|:-----|:------------|
| `previous_as` | `uint32` | The AS number that was configured before this call (0 if none). |
| `previous_router_id` | `string` | The router ID that was configured before this call (empty if none). |

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `FAILED_PRECONDITION` | Owner is not registered. |
| `INVALID_ARGUMENT` | Invalid AS number or router ID format. |

**Example:**

```go
resp, err := client.ConfigureBGP(ctx, &v1.ConfigureBGPRequest{
    Owner:    "admin",
    Token:    "admin-test-token-2024",
    LocalAs:  65000,
    RouterId: "10.0.0.1",
})
fmt.Printf("Previous: AS=%d router_id=%s\n", resp.PreviousAs, resp.PreviousRouterId)
```

---

## Peer Management

### ApplyPeer

Creates or updates a BGP peer. If the peer already exists with the same parameters, this is a no-op (idempotent). If parameters differ, the peer configuration is updated.

**Request: `ApplyPeerRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `peer` | `BGPPeer` | Full peer configuration. **Required.** |

**`BGPPeer` message:**

| Field | Type | Description |
|:------|:-----|:------------|
| `neighbor_address` | `string` | Peer IP address. **Required.** |
| `remote_as` | `uint32` | Remote autonomous system number. **Required.** |
| `peer_type` | `PeerType` | `PEER_TYPE_EXTERNAL` (eBGP) or `PEER_TYPE_INTERNAL` (iBGP). Default: `PEER_TYPE_UNSPECIFIED`. |
| `keepalive` | `uint32` | Keepalive interval in seconds. Default: 0 (use FRR default). |
| `hold_time` | `uint32` | Hold time in seconds. Must be >= 3 * `keepalive` if both are set. Default: 0 (use FRR default). |
| `bfd_enabled` | `bool` | Enable BFD failure detection for this peer. |
| `description` | `string` | Human-readable peer description. |
| `address_families` | `repeated AddressFamily` | `ADDRESS_FAMILY_IPV4_UNICAST` and/or `ADDRESS_FAMILY_IPV6_UNICAST`. |
| `source_address` | `string` | Source address for BGP update messages. |
| `ebgp_multihop` | `uint32` | eBGP multihop TTL (0 = disabled). |
| `password` | `string` | MD5 password for the BGP session. |
| `max_prefix` | `uint32` | Maximum number of prefixes to accept from this peer (0 = use default of 1000). |

**Response: `ApplyPeerResponse`**

Empty message. Success is indicated by a non-error response.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `FAILED_PRECONDITION` | Owner is not registered, or BGP is not configured. |
| `INVALID_ARGUMENT` | Missing neighbor address, invalid AS, or `hold_time` < 3 * `keepalive`. |

**Example:**

```go
_, err := client.ApplyPeer(ctx, &v1.ApplyPeerRequest{
    Owner: "novaedge",
    Token: "novaedge-test-token-2024",
    Peer: &v1.BGPPeer{
        NeighborAddress: "10.0.0.2",
        RemoteAs:        65001,
        PeerType:        v1.PeerType_PEER_TYPE_EXTERNAL,
        Keepalive:       10,
        HoldTime:        30,
        BfdEnabled:      true,
        Description:     "spine-01",
        AddressFamilies: []v1.AddressFamily{
            v1.AddressFamily_ADDRESS_FAMILY_IPV4_UNICAST,
        },
        MaxPrefix: 500,
    },
})
```

---

### RemovePeer

Removes a BGP peer and tears down the session in FRR.

**Request: `RemovePeerRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `neighbor_address` | `string` | IP address of the peer to remove. **Required.** |

**Response: `RemovePeerResponse`**

Empty message.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `NOT_FOUND` | No peer with this address exists for this owner. |
| `FAILED_PRECONDITION` | Owner is not registered. |

**Example:**

```go
_, err := client.RemovePeer(ctx, &v1.RemovePeerRequest{
    Owner:           "novaedge",
    Token:           "novaedge-test-token-2024",
    NeighborAddress: "10.0.0.2",
})
```

---

## Prefix Advertisement

### AdvertisePrefix

Advertises a network prefix via BGP or OSPF. The prefix is validated against the owner's configured prefix policy before being accepted.

**Request: `AdvertisePrefixRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `prefix` | `string` | CIDR notation prefix (e.g. `10.244.1.0/24`). **Required.** |
| `protocol` | `Protocol` | `PROTOCOL_BGP` or `PROTOCOL_OSPF`. **Required.** |
| `attributes` | `PrefixAttributes` | Optional BGP path attributes. |

**`PrefixAttributes` message:**

| Field | Type | Description |
|:------|:-----|:------------|
| `local_preference` | `uint32` | BGP local preference (0 = use default). |
| `communities` | `repeated string` | BGP community strings (e.g. `65000:100`). |
| `med` | `uint32` | Multi-Exit Discriminator (0 = not set). |
| `next_hop` | `string` | Override next-hop address (empty = use default). |

**Response: `AdvertisePrefixResponse`**

Empty message.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `FAILED_PRECONDITION` | Owner is not registered. |
| `INVALID_ARGUMENT` | Invalid CIDR format or unspecified protocol. |
| `PERMISSION_DENIED` | Prefix violates the owner's configured prefix policy. |

**Example:**

```go
_, err := client.AdvertisePrefix(ctx, &v1.AdvertisePrefixRequest{
    Owner:    "novaedge",
    Token:    "novaedge-test-token-2024",
    Prefix:   "10.244.1.0/24",
    Protocol: v1.Protocol_PROTOCOL_BGP,
    Attributes: &v1.PrefixAttributes{
        LocalPreference: 200,
        Communities:     []string{"65000:100"},
    },
})
```

---

### WithdrawPrefix

Withdraws a previously advertised prefix.

**Request: `WithdrawPrefixRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `prefix` | `string` | CIDR notation prefix. **Required.** |
| `protocol` | `Protocol` | `PROTOCOL_BGP` or `PROTOCOL_OSPF`. **Required.** |

**Response: `WithdrawPrefixResponse`**

Empty message.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `NOT_FOUND` | Prefix is not currently advertised by this owner. |
| `FAILED_PRECONDITION` | Owner is not registered. |

**Example:**

```go
_, err := client.WithdrawPrefix(ctx, &v1.WithdrawPrefixRequest{
    Owner:    "novaedge",
    Token:    "novaedge-test-token-2024",
    Prefix:   "10.244.1.0/24",
    Protocol: v1.Protocol_PROTOCOL_BGP,
})
```

---

## BFD

### EnableBFD

Enables Bidirectional Forwarding Detection for a peer address. BFD provides sub-second failure detection for BGP sessions.

**Request: `EnableBFDRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `peer_address` | `string` | IP address of the peer. **Required.** |
| `min_rx_ms` | `uint32` | Minimum receive interval in milliseconds (0 = FRR default). |
| `min_tx_ms` | `uint32` | Minimum transmit interval in milliseconds (0 = FRR default). |
| `detect_multiplier` | `uint32` | Detection multiplier (0 = FRR default, typically 3). |
| `interface_name` | `string` | Bind BFD session to a specific interface (empty = auto). |

**Response: `EnableBFDResponse`**

Empty message.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `FAILED_PRECONDITION` | Owner is not registered. |
| `INVALID_ARGUMENT` | Missing peer address. |

**Example:**

```go
_, err := client.EnableBFD(ctx, &v1.EnableBFDRequest{
    Owner:            "novaedge",
    Token:            "novaedge-test-token-2024",
    PeerAddress:      "10.0.0.2",
    MinRxMs:          300,
    MinTxMs:          300,
    DetectMultiplier: 3,
})
```

---

### DisableBFD

Disables BFD for a peer address.

**Request: `DisableBFDRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `peer_address` | `string` | IP address of the peer. **Required.** |

**Response: `DisableBFDResponse`**

Empty message.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `NOT_FOUND` | No BFD session exists for this peer address and owner. |
| `FAILED_PRECONDITION` | Owner is not registered. |

**Example:**

```go
_, err := client.DisableBFD(ctx, &v1.DisableBFDRequest{
    Owner:       "novaedge",
    Token:       "novaedge-test-token-2024",
    PeerAddress: "10.0.0.2",
})
```

---

## OSPF

### EnableOSPF

Enables OSPF on a network interface and assigns it to an OSPF area.

**Request: `EnableOSPFRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `interface_name` | `string` | Network interface name (e.g. `eth0`). **Required.** |
| `area_id` | `string` | OSPF area in dotted-quad format (e.g. `0.0.0.0`). **Required.** |
| `passive` | `bool` | If `true`, advertise the interface's subnets but do not form adjacencies. |
| `cost` | `uint32` | OSPF cost metric (0 = auto-calculated from bandwidth). |
| `hello_interval` | `uint32` | Hello packet interval in seconds (0 = OSPF default, 10s). |
| `dead_interval` | `uint32` | Dead interval in seconds. Must be >= `hello_interval` if both are set (0 = OSPF default, 40s). |

**Response: `EnableOSPFResponse`**

Empty message.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `FAILED_PRECONDITION` | Owner is not registered. |
| `INVALID_ARGUMENT` | Missing interface name, invalid area ID, or `dead_interval` < `hello_interval`. |

**Example:**

```go
_, err := client.EnableOSPF(ctx, &v1.EnableOSPFRequest{
    Owner:         "novanet",
    Token:         "novanet-test-token-2024",
    InterfaceName: "eth0",
    AreaId:        "0.0.0.0",
    Passive:       false,
    Cost:          10,
    HelloInterval: 10,
    DeadInterval:  40,
})
```

---

### DisableOSPF

Disables OSPF on a network interface.

**Request: `DisableOSPFRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner` | `string` | Owner identity. **Required.** |
| `token` | `string` | Authentication token. **Required.** |
| `interface_name` | `string` | Network interface name. **Required.** |

**Response: `DisableOSPFResponse`**

Empty message.

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAUTHENTICATED` | Invalid token. |
| `NOT_FOUND` | OSPF is not enabled on this interface for this owner. |
| `FAILED_PRECONDITION` | Owner is not registered. |

**Example:**

```go
_, err := client.DisableOSPF(ctx, &v1.DisableOSPFRequest{
    Owner:         "novanet",
    Token:         "novanet-test-token-2024",
    InterfaceName: "eth0",
})
```

---

## Observability

### GetStatus

Returns the full routing state of the agent. Results can be filtered by owner.

**Request: `GetStatusRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner_filter` | `string` | If set, only return state belonging to this owner. Empty returns all. |

**Response: `GetStatusResponse`**

| Field | Type | Description |
|:------|:-----|:------------|
| `peers` | `repeated PeerStatus` | BGP peer states. |
| `prefixes` | `repeated PrefixStatus` | Advertised prefix states. |
| `bfd_sessions` | `repeated BFDSessionStatus` | BFD session states. |
| `ospf_interfaces` | `repeated OSPFInterfaceStatus` | OSPF interface states. |
| `frr_status` | `FRRStatus` | FRR daemon connection and version info. |

**`PeerStatus`:**

| Field | Type | Description |
|:------|:-----|:------------|
| `neighbor_address` | `string` | Peer IP address. |
| `remote_as` | `uint32` | Remote AS number. |
| `state` | `string` | BGP session state (e.g. `Established`, `Active`, `Connect`). |
| `owner` | `string` | Owning identity. |
| `prefixes_received` | `uint32` | Number of prefixes received from this peer. |
| `prefixes_sent` | `uint32` | Number of prefixes sent to this peer. |
| `uptime` | `string` | Time since session established. |
| `bfd_enabled` | `bool` | Whether BFD is active on this peer. |
| `bfd_status` | `string` | BFD session state (e.g. `Up`, `Down`). |
| `description` | `string` | Peer description. |

**`PrefixStatus`:**

| Field | Type | Description |
|:------|:-----|:------------|
| `prefix` | `string` | CIDR prefix. |
| `protocol` | `Protocol` | `PROTOCOL_BGP` or `PROTOCOL_OSPF`. |
| `owner` | `string` | Owning identity. |
| `state` | `string` | Advertisement state (e.g. `advertised`, `pending`, `withdrawn`). |

**`BFDSessionStatus`:**

| Field | Type | Description |
|:------|:-----|:------------|
| `peer_address` | `string` | Peer IP address. |
| `state` | `string` | BFD state (e.g. `Up`, `Down`, `Init`). |
| `owner` | `string` | Owning identity. |
| `min_rx_ms` | `uint32` | Configured minimum RX interval. |
| `min_tx_ms` | `uint32` | Configured minimum TX interval. |
| `detect_multiplier` | `uint32` | Detection multiplier. |
| `uptime` | `string` | Time since session established. |

**`OSPFInterfaceStatus`:**

| Field | Type | Description |
|:------|:-----|:------------|
| `interface_name` | `string` | Interface name. |
| `area_id` | `string` | OSPF area ID. |
| `state` | `string` | Interface state (e.g. `DR`, `BDR`, `DROther`, `Point-to-Point`). |
| `owner` | `string` | Owning identity. |
| `neighbor_count` | `uint32` | Number of OSPF neighbors on this interface. |
| `cost` | `uint32` | OSPF cost metric. |

**`FRRStatus`:**

| Field | Type | Description |
|:------|:-----|:------------|
| `version` | `string` | FRR version string (e.g. `10.5.1`). |
| `connected` | `bool` | Whether the agent is connected to FRR via VTY socket. |
| `uptime` | `string` | FRR process uptime. |

**Error codes:**

| Code | Condition |
|:-----|:----------|
| `UNAVAILABLE` | Agent cannot reach FRR. |

**Example:**

```go
resp, err := client.GetStatus(ctx, &v1.GetStatusRequest{
    OwnerFilter: "novaedge",
})
for _, p := range resp.Peers {
    fmt.Printf("peer=%s state=%s\n", p.NeighborAddress, p.State)
}
```

---

### StreamEvents

Opens a server-streaming RPC that pushes real-time routing events. The stream remains open until the client disconnects.

**Request: `StreamEventsRequest`**

| Field | Type | Description |
|:------|:-----|:------------|
| `owner_filter` | `string` | If set, only receive events for this owner. |
| `event_types` | `repeated string` | If non-empty, only receive these event types. |

**Response: `stream RouteEvent`**

| Field | Type | Description |
|:------|:-----|:------------|
| `type` | `EventType` | The event type enum value. |
| `detail` | `string` | Human-readable event description. |
| `timestamp_unix` | `int64` | Unix epoch timestamp of the event. |
| `owner` | `string` | Owner associated with the event (if applicable). |
| `metadata` | `map<string, string>` | Additional key-value pairs with event-specific data. |

**`EventType` enum:**

| Value | Name | Description |
|:------|:-----|:------------|
| 0 | `EVENT_TYPE_UNSPECIFIED` | Default/unset value. |
| 1 | `EVENT_TYPE_PEER_UP` | BGP peer session reached Established state. |
| 2 | `EVENT_TYPE_PEER_DOWN` | BGP peer session dropped. |
| 3 | `EVENT_TYPE_PREFIX_ADVERTISED` | Prefix was successfully advertised to FRR. |
| 4 | `EVENT_TYPE_PREFIX_WITHDRAWN` | Prefix was withdrawn from FRR. |
| 5 | `EVENT_TYPE_BFD_UP` | BFD session reached Up state. |
| 6 | `EVENT_TYPE_BFD_DOWN` | BFD session went Down. |
| 7 | `EVENT_TYPE_OSPF_NEIGHBOR_UP` | OSPF neighbor adjacency formed. |
| 8 | `EVENT_TYPE_OSPF_NEIGHBOR_DOWN` | OSPF neighbor adjacency lost. |
| 9 | `EVENT_TYPE_FRR_CONNECTED` | Agent connected to FRR daemon. |
| 10 | `EVENT_TYPE_FRR_DISCONNECTED` | Agent lost connection to FRR daemon. |
| 11 | `EVENT_TYPE_OWNER_REGISTERED` | An owner registered a session. |
| 12 | `EVENT_TYPE_OWNER_DEREGISTERED` | An owner deregistered. |
| 13 | `EVENT_TYPE_POLICY_VIOLATION` | A request was rejected by prefix policy. |
| 14 | `EVENT_TYPE_BGP_CONFIG_CHANGED` | BGP global configuration (AS/router-id) changed. |

**Example:**

```go
stream, err := client.StreamEvents(ctx, &v1.StreamEventsRequest{
    OwnerFilter: "",
    EventTypes:  []string{"PEER_UP", "PEER_DOWN", "BFD_DOWN"},
})
for {
    event, err := stream.Recv()
    if err != nil {
        break
    }
    fmt.Printf("[%d] %s: %s (owner=%s)\n",
        event.TimestampUnix, event.Type, event.Detail, event.Owner)
}
```
