---
layout: default
title: CLI Reference
nav_order: 6
---

# novaroutectl CLI Reference
{: .no_toc }

`novaroutectl` is the command-line tool for inspecting and managing the NovaRoute agent. It communicates with the agent over the gRPC Unix domain socket.

<details open markdown="block">
  <summary>Table of contents</summary>
  {: .text-delta }
- TOC
{:toc}
</details>

---

## Global Flags

| Flag | Default | Description |
|:-----|:--------|:------------|
| `--socket` | `/run/novaroute/novaroute.sock` | Path to the NovaRoute Unix domain socket. |

All commands accept the `--socket` flag to override the default socket path:

```bash
novaroutectl --socket /tmp/novaroute.sock status
```

---

## Read-Only Commands

These commands query the agent's current state. They do not require `--owner` or `--token` (owner is used only as an optional filter).

### status

Show a summary of the overall NovaRoute agent status, including FRR connection state and counts of configured peers, prefixes, BFD sessions, and OSPF interfaces.

```
novaroutectl status [--owner OWNER]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | No | Filter results to a specific owner. |

**Example output:**

```
$ novaroutectl status
=== FRR Status ===
  Connection: connected
  Version:    10.5.1
  Uptime:     3h12m

=== Summary ===
  BGP Peers:        2
  Prefixes:         4
  BFD Sessions:     2
  OSPF Interfaces:  1
```

---

### peers

Display a table of all configured BGP peers and their session state.

```
novaroutectl peers [--owner OWNER]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | No | Filter peers by owner. |

**Example output:**

```
$ novaroutectl peers
NEIGHBOR      REMOTE AS  STATE        OWNER      PFX RECV  PFX SENT  BFD  UPTIME
--------      ---------  -----        -----      --------  --------  ---  ------
10.0.0.2      65001      Established  novaedge   12        3         Up   2h45m
10.0.0.3      65001      Established  novaedge   8         3         Up   2h44m
```

---

### prefixes

Display a table of all advertised prefixes and their current state.

```
novaroutectl prefixes [--owner OWNER]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | No | Filter prefixes by owner. |

**Example output:**

```
$ novaroutectl prefixes
PREFIX            PROTOCOL  STATE       OWNER
------            --------  -----       -----
10.244.1.0/24     BGP       advertised  novaedge
10.244.1.1/32     BGP       advertised  novaedge
192.168.50.0/24   OSPF      advertised  novanet
```

---

### bfd

Display a table of all BFD sessions and their current state.

```
novaroutectl bfd [--owner OWNER]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | No | Filter BFD sessions by owner. |

**Example output:**

```
$ novaroutectl bfd
PEER ADDRESS  STATE  OWNER      MIN RX (ms)  MIN TX (ms)  DETECT MULT  UPTIME
------------  -----  -----      -----------  -----------  -----------  ------
10.0.0.2      Up     novaedge   300          300          3            2h45m
10.0.0.3      Up     novaedge   300          300          3            2h44m
```

---

### ospf

Display a table of all OSPF-enabled interfaces and their current state.

```
novaroutectl ospf [--owner OWNER]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | No | Filter OSPF interfaces by owner. |

**Example output:**

```
$ novaroutectl ospf
INTERFACE  AREA       STATE  OWNER    NEIGHBORS  COST
---------  ----       -----  -----    ---------  ----
eth0       0.0.0.0    DR     novanet  2          10
```

---

### events

Stream real-time routing events from the agent. This command blocks and continuously prints events until interrupted with Ctrl+C.

```
novaroutectl events [--owner OWNER] [--types TYPE1,TYPE2,...]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | No | Filter events by owner. |
| `--types` | No | Comma-separated list of event types to subscribe to. |

Valid event types: `PEER_UP`, `PEER_DOWN`, `PREFIX_ADVERTISED`, `PREFIX_WITHDRAWN`, `BFD_UP`, `BFD_DOWN`, `OSPF_NEIGHBOR_UP`, `OSPF_NEIGHBOR_DOWN`, `FRR_CONNECTED`, `FRR_DISCONNECTED`, `OWNER_REGISTERED`, `OWNER_DEREGISTERED`, `POLICY_VIOLATION`, `BGP_CONFIG_CHANGED`.

**Example output:**

```
$ novaroutectl events --types PEER_UP,PEER_DOWN,BFD_DOWN
[2025-06-15T10:30:01Z] EVENT_TYPE_PEER_UP                  owner=novaedge    peer 10.0.0.2 AS 65001 reached Established
[2025-06-15T11:45:22Z] EVENT_TYPE_BFD_DOWN                 owner=novaedge    BFD session to 10.0.0.3 went Down
[2025-06-15T11:45:22Z] EVENT_TYPE_PEER_DOWN                owner=novaedge    peer 10.0.0.3 AS 65001 state changed to Idle
```

---

## Mutation Commands

These commands modify agent state. All require `--owner` and `--token` flags for authentication.

### register

Register an owner session with the agent. Must be called before any other mutation commands for that owner.

```
novaroutectl register --owner OWNER --token TOKEN [--reassert]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | Yes | Owner identity name. |
| `--token` | Yes | Authentication token. |
| `--reassert` | No | Reassert all previously stored intents to FRR. |

**Example:**

```
$ novaroutectl register --owner novaedge --token novaedge-test-token-2024 --reassert
Registered: session_id=sess-a1b2c3d4
  Current peers:    [10.0.0.2 10.0.0.3]
  Current prefixes: [10.244.1.0/24]
```

---

### deregister

Deregister an owner session. Optionally withdraw all routing intents.

```
novaroutectl deregister --owner OWNER --token TOKEN [--withdraw-all]
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | Yes | Owner identity name. |
| `--token` | Yes | Authentication token. |
| `--withdraw-all` | No | Withdraw all peers and prefixes belonging to this owner. |

**Example:**

```
$ novaroutectl deregister --owner novaedge --token novaedge-test-token-2024 --withdraw-all
Deregistered: owner=novaedge withdraw_all=true
```

---

### configure-bgp

Configure the global BGP autonomous system number and router ID.

```
novaroutectl configure-bgp --owner OWNER --token TOKEN --local-as AS --router-id ID
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | Yes | Owner identity name. |
| `--token` | Yes | Authentication token. |
| `--local-as` | Yes | Local AS number. |
| `--router-id` | Yes | BGP router ID in dotted-quad format. |

**Example:**

```
$ novaroutectl configure-bgp --owner admin --token admin-test-token-2024 \
    --local-as 65000 --router-id 10.0.0.1
BGP configured: AS=65000 router_id=10.0.0.1
  Previous: AS=0 router_id=
```

---

### apply-peer

Add or update a BGP peer. If the peer already exists, its configuration is updated in place.

```
novaroutectl apply-peer --owner OWNER --token TOKEN --neighbor ADDR --remote-as AS \
    [--peer-type external|internal] [--keepalive SEC] [--hold-time SEC] \
    [--bfd] [--ebgp-multihop HOP] [--password PASS] [--source-address ADDR] \
    [--max-prefix N] [--address-families ipv4-unicast,ipv6-unicast] \
    [--description DESC]
```

**Flags:**

| Flag | Required | Default | Description |
|:-----|:---------|:--------|:------------|
| `--owner` | Yes | | Owner identity name. |
| `--token` | Yes | | Authentication token. |
| `--neighbor` | Yes | | Neighbor IP address. |
| `--remote-as` | Yes | | Remote autonomous system number. |
| `--peer-type` | No | `external` | Peer type: `external` (eBGP), `internal` (iBGP). Also accepts `ebgp`/`ibgp`. |
| `--keepalive` | No | `0` | Keepalive interval in seconds. |
| `--hold-time` | No | `0` | Hold time in seconds. Must be >= 3x keepalive. |
| `--bfd` | No | `false` | Enable BFD for this peer. |
| `--ebgp-multihop` | No | `0` | eBGP multihop TTL (0 = disabled). |
| `--password` | No | | MD5 password for the BGP session. |
| `--source-address` | No | | Update source address. |
| `--max-prefix` | No | `0` | Maximum prefix limit (0 = default 1000). |
| `--address-families` | No | `ipv4-unicast` | Comma-separated list: `ipv4-unicast`, `ipv6-unicast`. |
| `--description` | No | | Human-readable peer description. |

**Example:**

```
$ novaroutectl apply-peer --owner novaedge --token novaedge-test-token-2024 \
    --neighbor 10.0.0.2 --remote-as 65001 --peer-type external \
    --keepalive 10 --hold-time 30 --bfd --description "spine-01" \
    --max-prefix 500
Peer applied: neighbor=10.0.0.2 remote_as=65001
```

---

### remove-peer

Remove a BGP peer and tear down the session.

```
novaroutectl remove-peer --owner OWNER --token TOKEN --neighbor ADDR
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | Yes | Owner identity name. |
| `--token` | Yes | Authentication token. |
| `--neighbor` | Yes | Neighbor IP address to remove. |

**Example:**

```
$ novaroutectl remove-peer --owner novaedge --token novaedge-test-token-2024 \
    --neighbor 10.0.0.2
Peer removed: neighbor=10.0.0.2
```

---

### advertise

Advertise a network prefix via BGP or OSPF.

```
novaroutectl advertise --owner OWNER --token TOKEN --prefix CIDR --protocol bgp|ospf \
    [--local-pref N] [--communities C1,C2,...] [--med N] [--next-hop ADDR]
```

**Flags:**

| Flag | Required | Default | Description |
|:-----|:---------|:--------|:------------|
| `--owner` | Yes | | Owner identity name. |
| `--token` | Yes | | Authentication token. |
| `--prefix` | Yes | | CIDR prefix to advertise (e.g. `10.244.1.0/24`). |
| `--protocol` | No | `bgp` | Routing protocol: `bgp` or `ospf`. |
| `--local-pref` | No | `0` | BGP local preference. |
| `--communities` | No | | Comma-separated BGP communities (e.g. `65000:100,65000:200`). |
| `--med` | No | `0` | Multi-Exit Discriminator. |
| `--next-hop` | No | | Override next-hop address. |

**Example:**

```
$ novaroutectl advertise --owner novaedge --token novaedge-test-token-2024 \
    --prefix 10.244.1.0/24 --protocol bgp \
    --local-pref 200 --communities 65000:100
Prefix advertised: 10.244.1.0/24 via bgp
```

---

### withdraw

Withdraw a previously advertised prefix.

```
novaroutectl withdraw --owner OWNER --token TOKEN --prefix CIDR --protocol bgp|ospf
```

**Flags:**

| Flag | Required | Default | Description |
|:-----|:---------|:--------|:------------|
| `--owner` | Yes | | Owner identity name. |
| `--token` | Yes | | Authentication token. |
| `--prefix` | Yes | | CIDR prefix to withdraw. |
| `--protocol` | No | `bgp` | Routing protocol: `bgp` or `ospf`. |

**Example:**

```
$ novaroutectl withdraw --owner novaedge --token novaedge-test-token-2024 \
    --prefix 10.244.1.0/24 --protocol bgp
Prefix withdrawn: 10.244.1.0/24 from bgp
```

---

### enable-bfd

Enable Bidirectional Forwarding Detection for a peer.

```
novaroutectl enable-bfd --owner OWNER --token TOKEN --peer ADDR \
    [--min-rx MS] [--min-tx MS] [--detect-multiplier N] [--interface IFACE]
```

**Flags:**

| Flag | Required | Default | Description |
|:-----|:---------|:--------|:------------|
| `--owner` | Yes | | Owner identity name. |
| `--token` | Yes | | Authentication token. |
| `--peer` | Yes | | Peer IP address. |
| `--min-rx` | No | `0` | Minimum receive interval in milliseconds. |
| `--min-tx` | No | `0` | Minimum transmit interval in milliseconds. |
| `--detect-multiplier` | No | `0` | Detection multiplier. |
| `--interface` | No | | Bind BFD session to an interface. |

**Example:**

```
$ novaroutectl enable-bfd --owner novaedge --token novaedge-test-token-2024 \
    --peer 10.0.0.2 --min-rx 300 --min-tx 300 --detect-multiplier 3
BFD enabled: peer=10.0.0.2
```

---

### disable-bfd

Disable BFD for a peer.

```
novaroutectl disable-bfd --owner OWNER --token TOKEN --peer ADDR
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | Yes | Owner identity name. |
| `--token` | Yes | Authentication token. |
| `--peer` | Yes | Peer IP address. |

**Example:**

```
$ novaroutectl disable-bfd --owner novaedge --token novaedge-test-token-2024 \
    --peer 10.0.0.2
BFD disabled: peer=10.0.0.2
```

---

### enable-ospf

Enable OSPF on a network interface.

```
novaroutectl enable-ospf --owner OWNER --token TOKEN --interface IFACE --area AREA \
    [--passive] [--cost N] [--hello-interval SEC] [--dead-interval SEC]
```

**Flags:**

| Flag | Required | Default | Description |
|:-----|:---------|:--------|:------------|
| `--owner` | Yes | | Owner identity name. |
| `--token` | Yes | | Authentication token. |
| `--interface` | Yes | | Network interface name (e.g. `eth0`). |
| `--area` | Yes | | OSPF area ID (e.g. `0.0.0.0`). |
| `--passive` | No | `false` | Set the interface as passive (no adjacency formation). |
| `--cost` | No | `0` | OSPF cost metric. |
| `--hello-interval` | No | `0` | Hello interval in seconds. |
| `--dead-interval` | No | `0` | Dead interval in seconds. Must be >= hello-interval. |

**Example:**

```
$ novaroutectl enable-ospf --owner novanet --token novanet-test-token-2024 \
    --interface eth0 --area 0.0.0.0 --cost 10
OSPF enabled: interface=eth0 area=0.0.0.0
```

---

### disable-ospf

Disable OSPF on a network interface.

```
novaroutectl disable-ospf --owner OWNER --token TOKEN --interface IFACE
```

**Flags:**

| Flag | Required | Description |
|:-----|:---------|:------------|
| `--owner` | Yes | Owner identity name. |
| `--token` | Yes | Authentication token. |
| `--interface` | Yes | Network interface name. |

**Example:**

```
$ novaroutectl disable-ospf --owner novanet --token novanet-test-token-2024 \
    --interface eth0
OSPF disabled: interface=eth0
```
