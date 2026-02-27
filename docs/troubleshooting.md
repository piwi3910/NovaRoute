---
layout: default
title: Troubleshooting
nav_order: 9
---

# Troubleshooting

This guide covers common issues, debugging techniques, and diagnostic commands for NovaRoute.

## Common Issues

### Agent Won't Start

**Symptoms:** The novaroute-agent container exits immediately or crashes during startup.

**Check config JSON validity:**

```bash
# Validate config syntax
cat /etc/novaroute/config.json | python3 -m json.tool
```

Common config mistakes include trailing commas, missing quotes around string values, and incorrect field names. The agent logs the exact parse error on startup failure.

**Check socket directory permissions:**

```bash
# The gRPC socket directory must exist and be writable
ls -la /run/novaroute/
```

If the socket directory does not exist or the agent process does not have write permission, the gRPC server cannot start. Ensure the directory is created via an `emptyDir` volume or init container.

**Check FRR sockets:**

```bash
# FRR VTY sockets must be present
ls -la /run/frr/
```

The agent expects FRR sockets in the configured socket directory. If they are missing, the agent starts but reports FRR as disconnected (the `/readyz` endpoint returns 503).

---

### FRR Not Connecting

**Symptoms:** `novaroute_frr_connected` is `0`, `/readyz` returns 503, logs show FRR connection errors.

**Check the frr-sock volume mount:**

```yaml
# Both containers must share the FRR socket volume
volumes:
  - name: frr-sock
    emptyDir: {}
```

Verify that both the NovaRoute agent container and the FRR sidecar container mount this volume at the same path (typically `/run/frr/`).

**Check that the FRR sidecar is running:**

```bash
kubectl get pods -l app=novaroute -o wide
kubectl describe pod <pod-name>
```

Look for the FRR container status. If it is in `CrashLoopBackOff`, inspect its logs with `kubectl logs <pod-name> -c frr`.

**Check for VTY sockets:**

```bash
kubectl exec <pod-name> -c frr -- ls -la /run/frr/
```

You should see socket files like `bgpd.vty`, `bfdd.vty`, and `ospfd.vty`. If these are absent, FRR has not started its daemons correctly. Check the FRR daemon configuration in `/etc/frr/daemons`.

---

### BGP Peer Not Establishing

**Symptoms:** Peer is configured but stays in `Idle` or `Active` state, never reaches `Established`.

**Check remote-as configuration:**

The remote AS number must match the actual AS of the remote peer. Mismatched AS numbers cause the BGP session to fail during the OPEN message exchange.

**Check timer values:**

BGP requires that `hold_time >= 3 * keepalive_interval`. If you set a hold time less than three times the keepalive interval, the intent is rejected with a validation error.

```json
{
  "keepalive_interval": 10,
  "hold_time": 30
}
```

**Check connectivity:**

```bash
# From the FRR sidecar, verify TCP connectivity to the peer on port 179
kubectl exec <pod-name> -c frr -- nc -zv <peer-ip> 179
```

If the connection fails, check network policies, firewall rules, and node-level connectivity.

**Check FRR logs:**

```bash
kubectl logs <pod-name> -c frr
```

Look for messages about OPEN failures, capability mismatches, or TCP connection resets.

---

### Prefixes Not Advertised

**Symptoms:** Prefixes are configured via intent but do not appear in BGP/OSPF announcements.

**Check the policy configuration:**

The `allowed_prefix_types` field in the owner policy controls which prefix types are permitted:

- `host_only` -- only /32 (IPv4) and /128 (IPv6) host routes
- `subnet` -- any prefix length
- `any` -- no restriction on prefix type

If the policy is set to `host_only` and you attempt to advertise a /24, the intent is rejected.

**Check the CIDR allowlist:**

If the owner policy specifies `allowed_cidrs`, the advertised prefix must fall within one of the allowed CIDR ranges. Prefixes outside the allowlist are rejected with a `prefix_denied` policy violation.

**Check the protocol field:**

When creating a prefix intent, ensure the `protocol` field is set correctly (`bgp` or `ospf`). A prefix intended for OSPF advertisement does not appear in BGP and vice versa.

---

### Policy Violations

**Symptoms:** Requests return `PermissionDenied` errors, `novaroute_policy_violations_total` is increasing.

**`invalid_token`:** The owner token in the request does not match the token registered for that owner. Verify the token in your client configuration.

**`peer_operation_denied`:** The owner policy does not allow BGP peer operations. Check `allow_peer_operations` in the policy config.

**`bfd_operation_denied`:** The owner policy does not allow BFD session operations. Check `allow_bfd_operations` in the policy config.

**`ospf_operation_denied`:** The owner policy does not allow OSPF interface operations. Check `allow_ospf_operations` in the policy config.

**`prefix_denied`:** The prefix is not within the allowed CIDRs or does not match the allowed prefix type. Review `allowed_cidrs` and `allowed_prefix_types`.

**`conflict`:** Another owner already owns the resource being requested. Each BGP peer, prefix, BFD session, or OSPF interface can only be owned by one owner at a time.

---

### Events Not Streaming

**Symptoms:** `novaroutectl events` shows no output, or the `StreamEvents` RPC returns no data.

**Check event type names:**

Event types must use the exact enum format with the `EVENT_TYPE_` prefix:

```
EVENT_TYPE_PEER_UP        (correct)
PEER_UP                   (incorrect)
peer_up                   (incorrect)
```

**Check the owner filter:**

If you specify an `--owner` filter, only events for that exact owner are returned. Verify spelling and case.

**Check for dropped events:**

If `novaroute_events_dropped_total` is increasing, subscribers are too slow to process events. Reduce the volume of events by using filters, or increase the processing speed of your subscriber.

---

### Metrics Not Scraped

**Symptoms:** Prometheus does not show NovaRoute metrics.

**Check the metrics address config:**

Verify that `metrics_address` is set in config.json. The default is `:9102`.

```json
{
  "metrics_address": ":9102"
}
```

**Check Prometheus scrape annotations:**

Ensure the pod has the correct annotations:

```yaml
annotations:
  prometheus.io/scrape: "true"
  prometheus.io/port: "9102"
  prometheus.io/path: "/metrics"
```

**Check network access:**

If using a network policy, ensure Prometheus can reach port 9102 on the NovaRoute pod.

---

### Reconciler Issues

**Symptoms:** Intended state and FRR state drift apart, logs show reconciliation errors.

**Check the reconcile cycle duration metric:**

```promql
histogram_quantile(0.99, rate(novaroute_reconcile_cycle_duration_seconds_bucket[5m]))
```

If cycle durations are consistently high, the reconciler may be struggling to converge. This often indicates FRR connectivity problems or a large number of intents.

**Check the logs for reconciliation errors:**

Look for log lines containing `"reconciliation completed with errors"`. These indicate that one or more FRR transactions failed during a reconcile cycle.

```bash
kubectl logs <pod-name> -c novaroute-agent | grep "reconciliation"
```

**Check FRR transaction metrics:**

A high `novaroute_frr_transactions_total{result="failure"}` rate usually means FRR is rejecting configuration commands. Inspect the FRR logs for details on which commands are failing.

## Log Levels

Set the log level in config.json using the `log_level` field:

```json
{
  "log_level": "debug"
}
```

Available levels (from most to least verbose):

| Level | Use Case |
|-------|----------|
| `debug` | Full detail including intent diffs, FRR commands, and reconciler state. Use for development and active debugging. |
| `info` | Normal operation. Logs startup, owner registration, peer changes, and reconcile summaries. |
| `warn` | Warnings about non-fatal issues such as slow subscribers or transient FRR errors. |
| `error` | Only errors that require attention, such as persistent FRR failures or configuration problems. |

## Useful Debug Commands

### novaroutectl

```bash
# Check agent and FRR status
novaroutectl status

# List all BGP peers and their state
novaroutectl peers

# Stream live events
novaroutectl events

# Stream events filtered by owner
novaroutectl events --owner=my-controller

# Stream events filtered by type
novaroutectl events --type=EVENT_TYPE_PEER_UP --type=EVENT_TYPE_PEER_DOWN
```

### Direct FRR Debugging

Exec into the FRR sidecar and use `vtysh` for direct inspection:

```bash
# Open a shell in the FRR sidecar
kubectl exec -it <pod-name> -c frr -- /bin/sh

# Launch vtysh
vtysh
```

Common vtysh commands:

```
# BGP summary showing all peers and their state
show bgp summary

# Detailed info for a specific peer
show bgp neighbor <peer-ip>

# Advertised routes
show bgp neighbor <peer-ip> advertised-routes

# BFD session status
show bfd peers

# BFD peer details
show bfd peer <peer-ip>

# OSPF neighbor table
show ip ospf neighbor

# OSPF interface status
show ip ospf interface

# Full running configuration
show running-config
```

### Kubernetes Debugging

```bash
# Pod status and events
kubectl describe pod <pod-name>

# Agent container logs
kubectl logs <pod-name> -c novaroute-agent

# FRR sidecar logs
kubectl logs <pod-name> -c frr

# Check readiness endpoint directly
kubectl exec <pod-name> -c novaroute-agent -- wget -qO- http://localhost:9102/readyz

# Check metrics endpoint directly
kubectl exec <pod-name> -c novaroute-agent -- wget -qO- http://localhost:9102/metrics
```
