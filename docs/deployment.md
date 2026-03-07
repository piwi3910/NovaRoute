# Kubernetes Deployment Guide

This guide covers deploying NovaRoute as a DaemonSet on a Kubernetes cluster. NovaRoute runs on every node to provide local routing control via FRR.
---

## Architecture Overview

NovaRoute deploys as a DaemonSet with two containers per pod:

- **novaroute-agent** -- The routing control plane. Receives gRPC intents from clients (NovaEdge, NovaNet) and translates them into FRR configuration via the VTY socket interface.
- **frr** -- The FRR routing engine sidecar. Runs bgpd, ospfd, zebra, and mgmtd to handle the actual routing protocol sessions.

Both containers share a volume for the FRR VTY Unix sockets. The agent's gRPC socket is exposed to the host via a `hostPath` volume so that other pods and host processes can connect.

---

## Prerequisites

- Kubernetes 1.24 or later
- `kubectl` configured with cluster-admin access
- Container images accessible from your nodes (GHCR public registry or a private mirror)

---

## Namespace

All NovaRoute resources are deployed into a dedicated namespace:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: nova-system
  labels:
    app.kubernetes.io/name: novaroute
    app.kubernetes.io/part-of: novaroute
```

---

## ServiceAccount

The agent runs under a dedicated service account:

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: novaroute-agent
  namespace: nova-system
  labels:
    app.kubernetes.io/name: novaroute-agent
    app.kubernetes.io/component: agent
```

---

## ConfigMaps

### Agent Configuration

The `novaroute-config` ConfigMap provides the agent's JSON configuration. Adjust `bgp.local_as`, `bgp.router_id`, owner tokens, and prefix policies for your environment:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: novaroute-config
  namespace: nova-system
  labels:
    app.kubernetes.io/name: novaroute-agent
    app.kubernetes.io/component: config
data:
  config.json: |
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
          "token": "CHANGE-ME-novaedge-token",
          "allowed_prefixes": {
            "type": "host_only",
            "allowed_cidrs": ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"]
          }
        },
        "novanet": {
          "token": "CHANGE-ME-novanet-token",
          "allowed_prefixes": {
            "type": "subnet",
            "allowed_cidrs": ["10.244.0.0/16"]
          }
        },
        "admin": {
          "token": "CHANGE-ME-admin-token",
          "allowed_prefixes": {
            "type": "any",
            "allowed_cidrs": []
          }
        }
      },
      "log_level": "info",
      "metrics_address": ":9102",
      "disconnect_grace_period": 30
    }
```

Note: The `bgp` section is optional in the config. If omitted, BGP must be configured at runtime via the `ConfigureBGP` RPC (e.g., by NovaEdge during reconciliation). The actual `deploy/configmap.yaml` in the repository omits the `bgp` section for this reason.

**Configuration fields:**

| Field | Description |
|:------|:------------|
| `listen_socket` | Path for the agent's gRPC Unix socket. |
| `frr.socket_dir` | Directory where FRR VTY sockets are located. |
| `frr.connect_timeout` | Seconds to wait for initial FRR connection. |
| `frr.retry_interval` | Seconds between FRR reconnection attempts. |
| `bgp.local_as` | Default BGP autonomous system number. |
| `bgp.router_id` | Default BGP router ID (dotted-quad). |
| `owners.<name>.token` | Authentication token for this owner. |
| `owners.<name>.allowed_prefixes.type` | Prefix policy: `host_only` (/32 only), `subnet`, or `any`. |
| `owners.<name>.allowed_prefixes.allowed_cidrs` | CIDR ranges the owner is permitted to advertise within. |
| `log_level` | Log verbosity: `debug`, `info`, `warn`, `error`. |
| `metrics_address` | Address for the Prometheus metrics and health endpoint. |
| `disconnect_grace_period` | Seconds to wait before cleaning up intents after an owner disconnects. |

### FRR Bootstrap Configuration

The `novaroute-frr-bootstrap` ConfigMap provides the initial FRR daemon configuration. NovaRoute manages all runtime routing configuration dynamically -- this bootstrap just enables the required daemons:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: novaroute-frr-bootstrap
  namespace: nova-system
  labels:
    app.kubernetes.io/name: novaroute-agent
    app.kubernetes.io/component: frr-config
data:
  daemons: |
    bgpd=yes
    ospfd=yes
    ospf6d=no
    ripd=no
    ripngd=no
    isisd=no
    pimd=no
    pim6d=no
    ldpd=no
    nhrpd=no
    eigrpd=no
    babeld=no
    sharpd=no
    pbrd=no
    bfdd=yes
    fabricd=no
    vrrpd=no
    pathd=no
    mgmtd=yes
    mgmtd_options="--log syslog informational"
    zebra_options="-A 127.0.0.1 -s 90000000"
    bgpd_options="-A 127.0.0.1 -p 0"
    ospfd_options="-A 127.0.0.1"
    bfdd_options="-A 127.0.0.1"
    frr_profile="datacenter"
    MAX_FIB_ROUTES=1000000
    watchfrr_enable=yes
    watchfrr_options=""

  frr.conf: |
    frr version 10.5.1
    frr defaults datacenter
    log syslog informational
    line vty

  vtysh.conf: |
    service integrated-vtysh-config
```

The key daemons enabled are:
- **zebra** -- Always runs; manages the kernel routing table.
- **bgpd** -- BGP routing daemon, listening on localhost only (`-p 0` disables the TCP listener since peers connect directly).
- **bfdd** -- BFD daemon for sub-second failure detection on BGP sessions.
- **ospfd** -- OSPF routing daemon.
- **mgmtd** -- Management daemon for the northbound gRPC interface.

The `frr_profile="datacenter"` setting uses FRR's data center defaults (shorter timers, best-path behavior tuned for leaf-spine topologies).

---

## DaemonSet

The DaemonSet runs one pod per node with `hostNetwork: true` so that BGP, BFD, and OSPF sessions use the node's real network interfaces and IP addresses.

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: novaroute-agent
  namespace: nova-system
  labels:
    app.kubernetes.io/name: novaroute-agent
    app.kubernetes.io/component: agent
spec:
  selector:
    matchLabels:
      app: novaroute-agent
  updateStrategy:
    type: RollingUpdate
    rollingUpdate:
      maxUnavailable: 1
  template:
    metadata:
      labels:
        app: novaroute-agent
        app.kubernetes.io/name: novaroute-agent
        app.kubernetes.io/component: agent
      annotations:
        prometheus.io/scrape: "true"
        prometheus.io/port: "9102"
        prometheus.io/path: "/metrics"
    spec:
      hostNetwork: true
      dnsPolicy: ClusterFirstWithHostNet
      serviceAccountName: novaroute-agent
      terminationGracePeriodSeconds: 60
      tolerations:
        - operator: Exists
          effect: NoSchedule
        - operator: Exists
          effect: NoExecute
      containers:
        - name: novaroute-agent
          image: ghcr.io/azrtydxb/novaroute/novaroute-agent:latest
          imagePullPolicy: Always
          args:
            - --config=/etc/novaroute/config.json
          ports:
            - name: metrics
              containerPort: 9102
              protocol: TCP
          livenessProbe:
            httpGet:
              path: /healthz
              port: 9102
            initialDelaySeconds: 10
            periodSeconds: 15
            timeoutSeconds: 5
            failureThreshold: 3
          readinessProbe:
            httpGet:
              path: /healthz
              port: 9102
            initialDelaySeconds: 5
            periodSeconds: 10
            timeoutSeconds: 5
            failureThreshold: 3
          resources:
            requests:
              cpu: 50m
              memory: 64Mi
            limits:
              cpu: 200m
              memory: 128Mi
          securityContext:
            capabilities:
              add:
                - NET_ADMIN
          volumeMounts:
            - name: run
              mountPath: /run/novaroute
            - name: frr-sock
              mountPath: /run/frr
            - name: config
              mountPath: /etc/novaroute
              readOnly: true

        - name: frr
          image: ghcr.io/azrtydxb/novaroute/novaroute-frr:10.5.1
          imagePullPolicy: Always
          resources:
            requests:
              cpu: 100m
              memory: 128Mi
            limits:
              cpu: 500m
              memory: 256Mi
          securityContext:
            capabilities:
              add:
                - NET_ADMIN
                - NET_RAW
                - SYS_ADMIN
          volumeMounts:
            - name: frr-sock
              mountPath: /run/frr
            - name: frr-config
              mountPath: /etc/frr
              readOnly: true

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

### Container Details

#### novaroute-agent

| Property | Value |
|:---------|:------|
| Image | `ghcr.io/azrtydxb/novaroute/novaroute-agent:latest` |
| Entrypoint arg | `--config=/etc/novaroute/config.json` |
| Metrics port | `9102` (HTTP) |
| Health endpoint | `/healthz` on port `9102` |
| CPU request/limit | 50m / 200m |
| Memory request/limit | 64Mi / 128Mi |
| Capabilities | `NET_ADMIN` |

The agent needs `NET_ADMIN` for route table manipulation. It connects to FRR's VTY sockets in `/run/frr` and exposes its own gRPC socket at `/run/novaroute/novaroute.sock`.

#### frr (sidecar)

| Property | Value |
|:---------|:------|
| Image | `ghcr.io/azrtydxb/novaroute/novaroute-frr:10.5.1` |
| CPU request/limit | 100m / 500m |
| Memory request/limit | 128Mi / 256Mi |
| Capabilities | `NET_ADMIN`, `NET_RAW`, `SYS_ADMIN` |

FRR requires elevated capabilities:
- **NET_ADMIN** -- Modify the kernel routing table and network interface configuration.
- **NET_RAW** -- Send and receive raw packets (required for BFD and OSPF).
- **SYS_ADMIN** -- Required for certain network namespace operations.

### Volumes

| Volume | Type | Mount Path | Purpose |
|:-------|:-----|:-----------|:--------|
| `run` | `hostPath` (`/run/novaroute`) | `/run/novaroute` | Exposes the agent's gRPC Unix socket to host-level clients. |
| `frr-sock` | `emptyDir` | `/run/frr` | Shared VTY sockets between the agent and FRR containers. |
| `config` | `ConfigMap` (`novaroute-config`) | `/etc/novaroute` | Agent JSON configuration. |
| `frr-config` | `ConfigMap` (`novaroute-frr-bootstrap`) | `/etc/frr` | FRR daemon bootstrap files. |

### Tolerations

The DaemonSet tolerates all taints (`operator: Exists`) so that NovaRoute runs on every node including control plane nodes:

```yaml
tolerations:
  - operator: Exists
    effect: NoSchedule
  - operator: Exists
    effect: NoExecute
```

### Update Strategy

Rolling updates with `maxUnavailable: 1` ensure that at most one node loses routing control at a time during upgrades. The `terminationGracePeriodSeconds: 60` gives the agent time to gracefully shut down BGP sessions.

---

## Deploying

Apply the manifests in order:

```bash
# Create namespace, service account, and ConfigMaps
kubectl apply -f deploy/configmap.yaml

# Deploy the DaemonSet
kubectl apply -f deploy/daemonset.yaml
```

Verify the rollout:

```bash
# Check that pods are running on all nodes
kubectl -n nova-system get pods -o wide

# Check agent logs
kubectl -n nova-system logs -l app=novaroute-agent -c novaroute-agent

# Check FRR sidecar logs
kubectl -n nova-system logs -l app=novaroute-agent -c frr
```

---

## Health Checks and Monitoring

### Probes

The agent exposes an HTTP health endpoint at `/healthz` on port 9102:

- **Liveness probe** -- Restarts the agent container if it becomes unresponsive (checked every 15s after a 10s initial delay, 3 failures trigger restart).
- **Readiness probe** -- Removes the pod from service if the agent is not ready (checked every 10s after a 5s initial delay). Note: the DaemonSet manifest above uses `/healthz` for both probes; for a stricter readiness check that verifies FRR connectivity, use `/readyz` instead.

### Prometheus Metrics

The agent exposes Prometheus metrics at `/metrics` on port 9102. The DaemonSet template includes annotations for automatic Prometheus scraping:

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9102"
  prometheus.io/path: "/metrics"
```

---

## Security Considerations

### hostNetwork

NovaRoute requires `hostNetwork: true` because BGP, BFD, and OSPF operate directly on the node's network stack. These protocols bind to the node's IP addresses and must be reachable by external peers (e.g., Top-of-Rack switches). Running in an overlay network or behind a service IP would break protocol peering.

With `hostNetwork: true`, the DNS policy is set to `ClusterFirstWithHostNet` so that in-cluster DNS resolution still works.

### Linux Capabilities

Rather than running as fully privileged, the containers use fine-grained Linux capabilities:

- The **agent** container only needs `NET_ADMIN` for route manipulation.
- The **FRR** container needs `NET_ADMIN`, `NET_RAW`, and `SYS_ADMIN` for full routing protocol operation.

Neither container runs as privileged (`privileged: true` is not set).

### Token Authentication

Every mutating gRPC call requires a valid owner/token pair. Tokens are configured in the `novaroute-config` ConfigMap. For production deployments, use a Kubernetes Secret to store tokens and mount them into the agent container instead of embedding them in the ConfigMap.

---

## Docker Images

NovaRoute publishes multi-architecture container images (amd64 and arm64) to the GitHub Container Registry (GHCR).

| Image | Description |
|:------|:------------|
| `ghcr.io/azrtydxb/novaroute/novaroute-agent:latest` | The NovaRoute agent control plane. |
| `ghcr.io/azrtydxb/novaroute/novaroute-frr:10.5.1` | FRR routing engine with bgpd, ospfd, bfdd, zebra, and mgmtd. |

Images are built and published automatically on release tags. Use a pinned version tag (e.g., `novaroute-agent:v0.5.0`) in production rather than `latest`.

To pull images from GHCR in a private cluster, create an image pull secret:

```bash
kubectl -n nova-system create secret docker-registry ghcr-pull-secret \
    --docker-server=ghcr.io \
    --docker-username=YOUR_GITHUB_USERNAME \
    --docker-password=YOUR_GITHUB_PAT
```

The DaemonSet references this secret via `imagePullSecrets`:

```yaml
spec:
  template:
    spec:
      imagePullSecrets:
        - name: ghcr-pull-secret
```

---

## Customization

### Per-Node Router ID

In a multi-node cluster, each node needs a unique BGP router ID. Use a DaemonSet init container or a `fieldRef` to inject the node IP into the config, or have each client call `ConfigureBGP` with the correct node-local router ID at startup.

### Resource Tuning

The default resource limits are conservative. For large-scale deployments with many peers or prefixes, consider increasing the FRR container limits:

```yaml
resources:
  requests:
    cpu: 200m
    memory: 256Mi
  limits:
    cpu: "1"
    memory: 512Mi
```

### Log Level

Set `log_level` in the agent ConfigMap to control verbosity:

- `debug` -- Verbose logging including every FRR VTY command and response.
- `info` -- Standard operational logging (recommended for production).
- `warn` -- Only warnings and errors.
- `error` -- Only errors.
