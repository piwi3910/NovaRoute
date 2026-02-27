---
layout: default
title: Monitoring & Observability
nav_order: 8
---

# Monitoring & Observability

NovaRoute exposes Prometheus metrics, health endpoints, and a gRPC event stream to give you full visibility into the routing control plane.

## Prometheus Metrics

The metrics endpoint is served at `:9102/metrics` by default. Configure the listen address with the `metrics_address` field in your config JSON.

Add standard Prometheus scrape annotations to your pod:

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9102"
  prometheus.io/path: "/metrics"
```

### Counters

| Metric | Labels | Description |
|--------|--------|-------------|
| `novaroute_intents_total` | `owner`, `type`, `operation` | Intent operations counted by owner, type (`peer`, `prefix`, `bfd`, `ospf`), and operation (`set`, `remove`). |
| `novaroute_frr_transactions_total` | `result` | FRR vtysh operations by result (`success`, `failure`). |
| `novaroute_policy_violations_total` | `owner`, `reason` | Policy check failures by owner and reason (`invalid_token`, `peer_operation_denied`, `bfd_operation_denied`, `ospf_operation_denied`, `prefix_denied`, `conflict`). |
| `novaroute_events_total` | `type` | Events emitted by type (e.g. `EVENT_TYPE_PEER_UP`, `EVENT_TYPE_PEER_DOWN`). |
| `novaroute_events_dropped_total` | | Events dropped because a subscriber could not keep up. |
| `novaroute_monitoring_errors_total` | `protocol` | FRR state monitoring errors by protocol (`bgp`, `bfd`, `ospf`). |

### Gauges

| Metric | Labels | Description |
|--------|--------|-------------|
| `novaroute_active_peers` | `owner` | Current number of BGP peers per owner. |
| `novaroute_active_prefixes` | `owner`, `protocol` | Currently advertised prefixes per owner and protocol. |
| `novaroute_active_bfd_sessions` | `owner` | Active BFD sessions per owner. |
| `novaroute_active_ospf_interfaces` | `owner` | OSPF interfaces per owner. |
| `novaroute_registered_owners` | | Total number of registered owners. |
| `novaroute_frr_connected` | | FRR connection status. `1` means connected, `0` means disconnected. |

### Histograms

| Metric | Labels | Description |
|--------|--------|-------------|
| `novaroute_grpc_request_duration_seconds` | `method` | gRPC request latency per RPC method. |
| `novaroute_frr_transaction_duration_seconds` | | Latency of individual FRR vtysh operations. |
| `novaroute_reconcile_cycle_duration_seconds` | | Duration of each reconciliation loop cycle. |

## Example PromQL Queries

**Intent operations per second by type:**

```promql
rate(novaroute_intents_total[5m])
```

**FRR transaction failure rate:**

```promql
rate(novaroute_frr_transactions_total{result="failure"}[5m])
/ on() rate(novaroute_frr_transactions_total[5m])
```

**95th percentile gRPC latency per method:**

```promql
histogram_quantile(0.95, rate(novaroute_grpc_request_duration_seconds_bucket[5m]))
```

**Policy violation rate by reason:**

```promql
sum by (reason) (rate(novaroute_policy_violations_total[5m]))
```

**Active BGP peers across all owners:**

```promql
sum(novaroute_active_peers)
```

**Reconcile cycle 99th percentile duration:**

```promql
histogram_quantile(0.99, rate(novaroute_reconcile_cycle_duration_seconds_bucket[5m]))
```

## Suggested Grafana Panels

### Overview Row

- **Registered Owners** -- Stat panel showing `novaroute_registered_owners`.
- **FRR Connection Status** -- Stat panel with value mapping: `novaroute_frr_connected` where `1` = Connected (green) and `0` = Disconnected (red).
- **Active BGP Peers** -- Stat panel showing `sum(novaroute_active_peers)`.

### BGP / BFD / OSPF Row

- **Peers by Owner** -- Bar gauge of `novaroute_active_peers` grouped by `owner`.
- **Prefixes by Owner and Protocol** -- Table of `novaroute_active_prefixes` with `owner` and `protocol` columns.
- **BFD Sessions** -- Time series of `novaroute_active_bfd_sessions` over time.
- **OSPF Interfaces** -- Time series of `novaroute_active_ospf_interfaces` over time.

### Performance Row

- **gRPC Latency (p95)** -- Time series of `histogram_quantile(0.95, rate(novaroute_grpc_request_duration_seconds_bucket[5m]))` grouped by `method`.
- **Reconcile Cycle Duration (p99)** -- Time series of reconcile cycle histogram quantile.
- **FRR Transaction Duration (p95)** -- Time series of FRR operation latency histogram quantile.

### Error Rate Row

- **FRR Transaction Failures** -- Time series of `rate(novaroute_frr_transactions_total{result="failure"}[5m])`.
- **Policy Violations by Reason** -- Stacked time series of `rate(novaroute_policy_violations_total[5m])` grouped by `reason`.
- **Monitoring Errors by Protocol** -- Time series of `rate(novaroute_monitoring_errors_total[5m])` grouped by `protocol`.
- **Dropped Events** -- Time series of `rate(novaroute_events_dropped_total[5m])`.

## Alerting Rules

```yaml
groups:
  - name: novaroute
    rules:
      - alert: NovaRouteFRRDisconnected
        expr: novaroute_frr_connected == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "NovaRoute lost connection to FRR"
          description: "The FRR sidecar has been unreachable for more than 1 minute on {{ $labels.instance }}."

      - alert: NovaRouteHighPolicyViolations
        expr: rate(novaroute_policy_violations_total[5m]) > 1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "High rate of policy violations"
          description: "Owner {{ $labels.owner }} is generating policy violations (reason: {{ $labels.reason }}) at {{ $value }}/s."

      - alert: NovaRouteReconcileFailures
        expr: rate(novaroute_frr_transactions_total{result="failure"}[5m]) > 0.1
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "FRR transaction failures detected"
          description: "FRR vtysh transactions are failing at {{ $value }}/s on {{ $labels.instance }}."

      - alert: NovaRouteReconcileSlow
        expr: histogram_quantile(0.99, rate(novaroute_reconcile_cycle_duration_seconds_bucket[5m])) > 5
        for: 10m
        labels:
          severity: warning
        annotations:
          summary: "Reconciliation cycles are slow"
          description: "The p99 reconcile cycle duration exceeds 5 seconds on {{ $labels.instance }}."

      - alert: NovaRoutePeerDown
        expr: delta(novaroute_active_peers[5m]) < -1
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "BGP peers lost"
          description: "Owner {{ $labels.owner }} lost peers in the last 5 minutes on {{ $labels.instance }}."

      - alert: NovaRouteEventsDropped
        expr: rate(novaroute_events_dropped_total[5m]) > 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Events are being dropped"
          description: "Slow subscribers are causing event drops at {{ $value }}/s on {{ $labels.instance }}."
```

## Health Endpoints

NovaRoute exposes two HTTP health endpoints on the metrics port (`:9102`):

| Endpoint | Purpose | Healthy | Unhealthy |
|----------|---------|---------|-----------|
| `/healthz` | Liveness probe | Always returns `200 OK`. | N/A -- always healthy if the process is running. |
| `/readyz` | Readiness probe | `200 OK` when FRR is connected. | `503 Service Unavailable` when FRR is disconnected. |

Use these in your Kubernetes pod spec:

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 9102
  initialDelaySeconds: 5
  periodSeconds: 10
readinessProbe:
  httpGet:
    path: /readyz
    port: 9102
  initialDelaySeconds: 5
  periodSeconds: 5
```

## Event Streaming

The `StreamEvents` gRPC RPC provides a real-time stream of routing state changes. There are 15 event types available:

- `EVENT_TYPE_PEER_UP`
- `EVENT_TYPE_PEER_DOWN`
- `EVENT_TYPE_PEER_ADDED`
- `EVENT_TYPE_PEER_REMOVED`
- `EVENT_TYPE_PREFIX_ADDED`
- `EVENT_TYPE_PREFIX_REMOVED`
- `EVENT_TYPE_BFD_SESSION_UP`
- `EVENT_TYPE_BFD_SESSION_DOWN`
- `EVENT_TYPE_BFD_SESSION_ADDED`
- `EVENT_TYPE_BFD_SESSION_REMOVED`
- `EVENT_TYPE_OSPF_NEIGHBOR_UP`
- `EVENT_TYPE_OSPF_NEIGHBOR_DOWN`
- `EVENT_TYPE_OSPF_INTERFACE_ADDED`
- `EVENT_TYPE_OSPF_INTERFACE_REMOVED`
- `EVENT_TYPE_RECONCILE_ERROR`

### Filtering

You can filter the event stream by owner and/or event type in the `StreamEventsRequest`:

```protobuf
message StreamEventsRequest {
  string owner = 1;           // optional: only events for this owner
  repeated string types = 2;  // optional: only these event types
}
```

### CLI Usage

```bash
# Stream all events
novaroutectl events

# Stream events for a specific owner
novaroutectl events --owner=my-controller

# Stream only peer events
novaroutectl events --type=EVENT_TYPE_PEER_UP --type=EVENT_TYPE_PEER_DOWN
```

If a subscriber cannot consume events fast enough, events are dropped and counted by the `novaroute_events_dropped_total` metric.
