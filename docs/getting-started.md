---
layout: default
title: Getting Started
nav_order: 2
---

# Getting Started

This guide walks you through installing NovaRoute, creating a configuration, starting the agent, and setting up your first BGP peer.

---

## Prerequisites

| Requirement | Version | Notes |
|-------------|---------|-------|
| **Go** | 1.26+ | Required only for building from source |
| **FRR** | 10.x | The routing engine NovaRoute controls; runs as a sidecar in Kubernetes |
| **protoc** | 3.x+ | Required only if regenerating protobuf files from `api/v1/novaroute.proto` |
| **protoc-gen-go** | latest | Go protobuf plugin (`go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`) |
| **protoc-gen-go-grpc** | latest | Go gRPC plugin (`go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`) |

For Kubernetes deployments, you also need:
- A Kubernetes cluster with nodes that have direct network access to your BGP routers
- `kubectl` configured to access the cluster
- Permissions to create DaemonSets, ConfigMaps, and hostPath volumes

---

## Installation

### Option 1: Build from Source

Clone the repository and build both binaries:

```bash
git clone https://github.com/piwi3910/NovaRoute.git
cd NovaRoute
make build
```

This produces two binaries in the `bin/` directory:

| Binary | Purpose |
|--------|---------|
| `bin/novaroute-agent` | The main daemon that manages FRR |
| `bin/novaroutectl` | CLI tool for inspecting and controlling the agent |

To build only one of them:

```bash
make build-agent    # builds bin/novaroute-agent only
make build-ctl      # builds bin/novaroutectl only
```

To regenerate protobuf files after modifying `api/v1/novaroute.proto`:

```bash
make proto
```

To run the test suite:

```bash
make test
```

### Option 2: Docker Image

Pull the pre-built multi-architecture image:

```bash
docker pull ghcr.io/piwi3910/novaroute/novaroute-agent:latest
```

This image supports both `linux/amd64` and `linux/arm64`.

Run the agent with a config file:

```bash
docker run -d \
  --name novaroute-agent \
  --network host \
  -v /run/frr:/run/frr \
  -v /run/novaroute:/run/novaroute \
  -v /etc/novaroute:/etc/novaroute:ro \
  ghcr.io/piwi3910/novaroute/novaroute-agent:latest \
  --config=/etc/novaroute/config.json
```

### Option 3: Kubernetes DaemonSet

NovaRoute ships with a ready-to-use DaemonSet manifest in `deploy/daemonset.yaml`. The DaemonSet runs two containers per pod:

1. **novaroute-agent** -- the routing control plane
2. **frr** -- the FRR routing engine sidecar

Both containers run with `hostNetwork: true` because FRR needs to bind TCP 179 (BGP), send BFD packets, manage OSPF adjacencies, and modify the kernel FIB directly.

Apply the deployment:

```bash
# Create the namespace and ConfigMap first
kubectl apply -f deploy/configmap.yaml

# Deploy the DaemonSet
kubectl apply -f deploy/daemonset.yaml
```

Verify the pods are running:

```bash
kubectl get pods -l app=novaroute -o wide
```

---

## Quick Start

### Step 1: Create a Configuration File

Create `/etc/novaroute/config.json` with your BGP settings and at least one owner:

```json
{
  "listen_socket": "/run/novaroute/novaroute.sock",
  "frr": {
    "socket_dir": "/run/frr",
    "connect_timeout": 10,
    "retry_interval": 5
  },
  "bgp": {
    "local_as": 65011,
    "router_id": "192.168.100.10"
  },
  "owners": {
    "admin": {
      "token": "my-admin-token",
      "allowed_prefixes": {
        "type": "any"
      }
    }
  },
  "log_level": "info",
  "metrics_address": ":9102",
  "disconnect_grace_period": 30
}
```

See the [Configuration]({% link configuration.md %}) page for the full reference.

### Step 2: Start the Agent

If running from source (FRR must already be running on the node):

```bash
sudo bin/novaroute-agent --config=/etc/novaroute/config.json
```

The agent will:
1. Load and validate the configuration
2. Wait for FRR VTY sockets (`zebra.vty`, `bgpd.vty`) to appear in `/run/frr/`
3. Configure the BGP instance in FRR (`router bgp 65011`, `bgp router-id 192.168.100.10`)
4. Start the gRPC server on `/run/novaroute/novaroute.sock`
5. Start the reconciler (30-second periodic sync)
6. Start the Prometheus metrics server on `:9102`

### Step 3: Verify the Agent is Running

```bash
bin/novaroutectl status
```

Expected output:

```
=== FRR Status ===
  Connection: connected
  Version:    10.5.1
  Uptime:     0h0m12s

=== Summary ===
  BGP Peers:        0
  Prefixes:         0
  BFD Sessions:     0
  OSPF Interfaces:  0
```

You can also check the health endpoints:

```bash
curl http://localhost:9102/healthz    # liveness: always returns 200
curl http://localhost:9102/readyz     # readiness: returns 200 if FRR is connected
```

---

## Setting Up Your First BGP Peer

### Step 4: Register as an Owner

Before issuing any mutations, register your owner session:

```bash
bin/novaroutectl register \
  --owner admin \
  --token my-admin-token
```

### Step 5: Add a BGP Peer

Use the `apply-peer` command to establish a BGP session with your ToR switch or upstream router:

```bash
bin/novaroutectl apply-peer \
  --owner admin \
  --token my-admin-token \
  --neighbor 192.168.100.1 \
  --remote-as 65000 \
  --peer-type external \
  --keepalive 30 \
  --hold-time 90 \
  --address-families ipv4-unicast
```

This tells NovaRoute to configure FRR with:
- A BGP neighbor at `192.168.100.1` with remote AS 65000
- eBGP peering with 30s keepalive and 90s hold time
- IPv4 unicast address family activated

### Step 6: Advertise a Prefix

Advertise a VIP or network prefix through the new peer:

```bash
bin/novaroutectl advertise \
  --owner admin \
  --token my-admin-token \
  --prefix 10.0.0.100/32 \
  --protocol bgp \
  --local-pref 100
```

### Step 7: Verify the Setup

Check that the peer is established and the prefix is being advertised:

```bash
# View peer status
bin/novaroutectl peers

# View advertised prefixes
bin/novaroutectl prefixes

# Full status overview
bin/novaroutectl status
```

Example peers output:

```
NEIGHBOR         REMOTE AS  STATE        OWNER  PFX RECV  PFX SENT  BFD  UPTIME
--------         ---------  -----        -----  --------  --------  ---  ------
192.168.100.1    65000      Established  admin  5         1         no   0h2m30s
```

### Step 8: Stream Events (Optional)

Open a second terminal and stream real-time events to observe state changes:

```bash
bin/novaroutectl events --owner admin
```

This will display events like peer state transitions, prefix advertisements, and policy violations as they occur.

---

## Next Steps

- Read the [Architecture]({% link architecture.md %}) page to understand the reconciler, policy engine, and FRR integration
- See the [Configuration]({% link configuration.md %}) page for the full config reference, environment variables, and prefix policy types
- Set up multiple owners (e.g., `novaedge` with `host_only` policy) to see the policy engine in action
