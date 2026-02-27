---
layout: default
title: Configuration
nav_order: 4
---

# Configuration

NovaRoute agent reads a JSON configuration file at startup. The default path is `/etc/novaroute/config.json`, overridden via the `--config` flag.

---

## Configuration Reference

### Top-Level Fields

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `listen_socket` | string | `/run/novaroute/novaroute.sock` | No | Unix domain socket path for the gRPC API. Clients connect here. |
| `log_level` | string | `info` | No | Logging verbosity. Valid values: `debug`, `info`, `warn`, `error`. |
| `metrics_address` | string | `:9102` | No | Listen address for the Prometheus metrics HTTP server. Serves `/metrics`, `/healthz`, and `/readyz`. Note: the code default is `:9100`, but the recommended production value is `:9102` to avoid conflicts with node_exporter. |
| `disconnect_grace_period` | int | `0` | No | Seconds to wait before withdrawing an owner's intents after their gRPC connection drops. Set to 0 to withdraw immediately. A value of 30 gives clients time to reconnect after transient failures. |

### FRR Connection Settings (`frr.*`)

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `frr.socket_dir` | string | `/run/frr` | No | Directory containing FRR daemon VTY sockets (`zebra.vty`, `bgpd.vty`, etc.). This is the directory passed to `vtysh --vty_socket`. |
| `frr.connect_timeout` | int | `10` | No | Timeout in seconds when waiting for FRR VTY sockets to become available at startup. Must be a positive integer. |
| `frr.retry_interval` | int | `5` | No | Interval in seconds between retry attempts when FRR VTY sockets are not yet available. Must be a positive integer. |

### BGP Global Settings (`bgp.*`)

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `bgp.local_as` | uint32 | -- | **Yes** | Local autonomous system number. Used in `router bgp <AS>`. Must be greater than 0. Can be overridden at runtime via the `ConfigureBGP` RPC or the `NOVAROUTE_BGP_LOCAL_AS` environment variable. |
| `bgp.router_id` | string | -- | **Yes** | BGP router identifier in IPv4 address format (e.g., `10.0.0.1`). Used in `bgp router-id <ID>`. Supports `${VAR}` environment variable expansion (e.g., `${NODE_IP}`). Can be overridden by `NOVAROUTE_BGP_ROUTER_ID`. |

### Owner Definitions (`owners.*`)

The `owners` field is a map of owner name to owner configuration. At least one owner must be defined. Each owner entry has the following structure:

| Field | Type | Default | Required | Description |
|-------|------|---------|----------|-------------|
| `owners.<name>.token` | string | -- | **Yes** | Pre-shared authentication token for this owner. Every RPC from this owner must include this token. Supports `${VAR}` expansion (e.g., `${NOVAEDGE_TOKEN}`). Must not be empty after expansion. |
| `owners.<name>.allowed_prefixes.type` | string | -- | **Yes** | Prefix policy type. Controls what kinds of prefixes this owner may advertise. Valid values: `host_only`, `subnet`, `any`. See [Prefix Policy Types](#prefix-policy-types) below. |
| `owners.<name>.allowed_prefixes.allowed_cidrs` | []string | `[]` (empty) | No | Optional list of CIDR ranges that further restrict which prefixes can be advertised. If non-empty, every advertised prefix must fall within at least one of these CIDRs. Each entry must be a valid CIDR (e.g., `10.0.0.0/8`). See [CIDR Allowlist](#cidr-allowlist) below. |

---

## Complete Example

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
    "router_id": "${NODE_IP}"
  },
  "owners": {
    "novaedge": {
      "token": "${NOVAEDGE_TOKEN}",
      "allowed_prefixes": {
        "type": "host_only",
        "allowed_cidrs": [
          "10.0.0.0/8",
          "172.16.0.0/12",
          "192.168.0.0/16"
        ]
      }
    },
    "novanet": {
      "token": "${NOVANET_TOKEN}",
      "allowed_prefixes": {
        "type": "subnet",
        "allowed_cidrs": [
          "10.244.0.0/16"
        ]
      }
    },
    "admin": {
      "token": "${ADMIN_TOKEN}",
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

This configuration:
- Listens on the default Unix socket path
- Connects to FRR via VTY sockets in `/run/frr` with a 10-second timeout
- Sets up BGP AS 65011 with the router ID taken from the `NODE_IP` environment variable
- Defines three owners with different prefix policies (see below)
- Gives clients 30 seconds to reconnect before their intents are withdrawn
- Exports Prometheus metrics on port 9102

---

## Environment Variable Support

NovaRoute supports environment variable expansion in configuration values using the `${VAR}` syntax. If a referenced variable is not set, the placeholder is replaced with an empty string.

### Token Expansion

Owner tokens support `${VAR}` expansion, allowing you to keep secrets out of the config file:

```json
{
  "owners": {
    "novaedge": {
      "token": "${NOVAEDGE_TOKEN}"
    }
  }
}
```

Set the corresponding environment variable before starting the agent:

```bash
export NOVAEDGE_TOKEN="my-secret-token-value"
```

### Router ID Expansion

The `bgp.router_id` field supports `${VAR}` expansion, which is useful in Kubernetes where each node has a different IP:

```json
{
  "bgp": {
    "router_id": "${NODE_IP}"
  }
}
```

In a Kubernetes DaemonSet, set this from the downward API:

```yaml
env:
  - name: NODE_IP
    valueFrom:
      fieldRef:
        fieldPath: status.hostIP
```

### Override Environment Variables

Two environment variables override their corresponding config file values unconditionally when set, regardless of what the config file contains:

| Environment Variable | Overrides | Value Format |
|---------------------|-----------|--------------|
| `NOVAROUTE_BGP_LOCAL_AS` | `bgp.local_as` | Unsigned 32-bit integer (e.g., `65012`) |
| `NOVAROUTE_BGP_ROUTER_ID` | `bgp.router_id` | IPv4 address string (e.g., `10.0.0.5`) |

These are useful for per-node overrides in Kubernetes where each node needs a different AS number or router ID:

```yaml
env:
  - name: NOVAROUTE_BGP_LOCAL_AS
    value: "65012"
  - name: NOVAROUTE_BGP_ROUTER_ID
    valueFrom:
      fieldRef:
        fieldPath: status.hostIP
```

If `NOVAROUTE_BGP_LOCAL_AS` is set but cannot be parsed as a valid uint32, a warning is printed to stderr and the config file value is retained.

---

## Prefix Policy Types

The `allowed_prefixes.type` field controls what category of prefixes an owner is permitted to advertise. This is the primary safety mechanism that prevents clients from advertising routes outside their intended scope.

### `host_only`

Restricts the owner to advertising host routes only:
- **IPv4**: only `/32` prefixes (single IP addresses)
- **IPv6**: only `/128` prefixes (single IP addresses)

Any attempt to advertise a subnet (e.g., `10.0.0.0/24`) is rejected with a policy violation.

**Typical use case**: NovaEdge advertising virtual IP addresses for load-balanced services. Each VIP is a single IP, so `host_only` ensures NovaEdge cannot accidentally advertise entire subnets.

```json
{
  "novaedge": {
    "token": "${NOVAEDGE_TOKEN}",
    "allowed_prefixes": {
      "type": "host_only",
      "allowed_cidrs": ["10.0.0.0/8"]
    }
  }
}
```

### `subnet`

Restricts the owner to advertising subnet routes only:
- Prefix lengths from `/8` through `/28` are allowed
- Host routes (`/32` for IPv4, `/128` for IPv6) are rejected
- Very broad prefixes (shorter than `/8`) are also rejected

**Typical use case**: NovaNet advertising pod CIDR ranges and node subnets. These are always subnets, never individual host addresses.

```json
{
  "novanet": {
    "token": "${NOVANET_TOKEN}",
    "allowed_prefixes": {
      "type": "subnet",
      "allowed_cidrs": ["10.244.0.0/16"]
    }
  }
}
```

### `any`

No restrictions on prefix length. The owner can advertise any valid CIDR prefix.

**Typical use case**: The `admin` owner, used for manual operations and overrides. Since the admin token should be tightly controlled, allowing all prefix types is acceptable.

```json
{
  "admin": {
    "token": "${ADMIN_TOKEN}",
    "allowed_prefixes": {
      "type": "any"
    }
  }
}
```

### Summary Table

| Type | Allowed IPv4 Lengths | Allowed IPv6 Lengths | Typical Owner |
|------|---------------------|---------------------|---------------|
| `host_only` | `/32` only | `/128` only | NovaEdge (VIPs) |
| `subnet` | `/8` through `/28` | `/8` through `/128` (excluding `/128`) | NovaNet (pod CIDRs) |
| `any` | All | All | Admin |

---

## CIDR Allowlist

The optional `allowed_cidrs` field provides an additional layer of restriction beyond the prefix type. When configured, every advertised prefix must fall within at least one of the listed CIDR ranges.

### How It Works

When a client calls `AdvertisePrefix` with a prefix like `10.0.0.100/32`:

1. The policy engine first checks the prefix type (`host_only` allows `/32` -- pass)
2. Then it checks the CIDR allowlist: is `10.0.0.100/32` contained within any of the `allowed_cidrs`?
3. If `allowed_cidrs` contains `10.0.0.0/8`, then `10.0.0.100/32` is within that range -- pass
4. If `allowed_cidrs` contains only `172.16.0.0/12`, then `10.0.0.100/32` is not within that range -- rejected

### Containment Logic

A prefix is "contained within" a CIDR if the prefix's network address falls within the CIDR's range. For example:

| Prefix | Allowlist CIDR | Contained? |
|--------|---------------|------------|
| `10.0.0.100/32` | `10.0.0.0/8` | Yes |
| `10.244.1.0/24` | `10.244.0.0/16` | Yes |
| `172.16.5.0/24` | `10.0.0.0/8` | No |
| `10.0.0.100/32` | `10.0.0.0/8`, `172.16.0.0/12` | Yes (matches first) |

### When to Use

- **With `host_only`**: restrict which VIP ranges NovaEdge can advertise (e.g., only RFC 1918 addresses)
- **With `subnet`**: restrict which pod CIDRs NovaNet can advertise (e.g., only the cluster's pod CIDR)
- **With `any`**: restrict the admin to specific ranges (rarely needed but available)
- **Empty list**: no CIDR restriction; only the prefix type is enforced

---

## Validation Rules

The agent validates the configuration at startup and exits with an error if any rule is violated:

| Rule | Error Message |
|------|---------------|
| `listen_socket` must not be empty | `listen_socket must not be empty` |
| `frr.socket_dir` must not be empty | `frr.socket_dir must not be empty` |
| `frr.connect_timeout` must be positive | `frr.connect_timeout must be positive, got <value>` |
| `frr.retry_interval` must be positive | `frr.retry_interval must be positive, got <value>` |
| `bgp.local_as` must be greater than 0 | `bgp.local_as must be greater than 0` |
| `bgp.router_id` must be a valid IP address | `bgp.router_id "<value>" is not a valid IP address` |
| At least one owner must be configured | `at least one owner must be configured` |
| Each owner's token must not be empty | `owner "<name>": token must not be empty` |
| Each owner's prefix type must be valid | `owner "<name>": unknown allowed_prefixes.type "<value>"` |
| Each CIDR in `allowed_cidrs` must be valid | `owner "<name>": allowed_prefixes.allowed_cidrs[<i>] "<value>" is not a valid CIDR` |
| `log_level` must be one of: debug, info, warn, error | `log_level "<value>" is not valid` |
| `disconnect_grace_period` must not be negative | `disconnect_grace_period must not be negative, got <value>` |

Note: Environment variable expansion (`ExpandEnvVars`) runs before validation. If a `${VAR}` token expands to an empty string and the field is required (e.g., owner token), validation will catch the error.

---

## Runtime BGP Configuration

In addition to static config, the BGP AS number and router ID can be changed at runtime via the `ConfigureBGP` RPC (or `novaroutectl configure-bgp`). This is how controllers like NovaEdge set per-node AS numbers for eBGP deployments.

A runtime change triggers:
1. `no router bgp <old-AS>` in FRR (tears down all sessions)
2. `router bgp <new-AS>` with the new router ID
3. The reconciler re-applies all existing peers and prefixes under the new AS

```bash
bin/novaroutectl configure-bgp \
  --owner admin \
  --token my-admin-token \
  --local-as 65012 \
  --router-id 10.0.0.5
```
