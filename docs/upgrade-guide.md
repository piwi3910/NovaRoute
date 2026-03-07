# Upgrade Guide

This guide covers the procedures, compatibility requirements, and troubleshooting steps for upgrading NovaRoute in a Kubernetes cluster.

---

## Pre-Upgrade Checklist

Before starting an upgrade, verify the following:

### 1. Back Up CRDs

If the NovaRoute operator is installed, export all custom resource definitions and custom resources:

```bash
# Export CRDs
kubectl get crds -o yaml | grep -A 1000 "novaroute" > novaroute-crds-backup.yaml

# Export all NovaRoute custom resources (if using the operator)
kubectl get novarouteclusters -A -o yaml > novaroute-clusters-backup.yaml
```

### 2. Check FRR State

Verify that FRR daemons are healthy on every node before upgrading:

```bash
# Check FRR container status across all nodes
kubectl -n nova-system get pods -l app=novaroute-agent -o wide

# Verify FRR daemons are running inside each pod
kubectl -n nova-system exec <pod-name> -c frr -- vtysh -c "show logging"
```

### 3. Verify BGP Sessions Are Stable

All BGP sessions should be in `Established` state with no recent flaps:

```bash
# Check BGP summary on each node
kubectl -n nova-system exec <pod-name> -c frr -- vtysh -c "show bgp summary"

# Verify no recent peer state changes
novaroutectl status
novaroutectl peers
```

Look for:

- All peers in `Established` state
- Stable uptime (no recent resets)
- No increasing `novaroute_policy_violations_total` counter

### 4. Check BFD Sessions

If BFD is in use, verify sessions are up:

```bash
kubectl -n nova-system exec <pod-name> -c frr -- vtysh -c "show bfd peers"
```

### 5. Review Release Notes

Always read the release notes for the target version before upgrading. Pay attention to:

- Breaking changes in configuration format
- CRD schema changes
- Required manual migration steps
- Minimum Kubernetes version changes

---

## Version Compatibility Matrix

The following table shows which versions of the NovaRoute components are compatible with each other:

| Operator Version | Agent Version | FRR Version | Min. Kubernetes |
|:-----------------|:--------------|:------------|:----------------|
| 0.1.0            | 0.2.0         | 10.5.1      | 1.24            |

!!! note
    The operator chart version (`0.1.0`) and the agent chart version (`0.1.0`, appVersion `0.2.0`) are independently versioned. Always consult the release notes for the exact compatible pairings.

### FRR Version Policy

NovaRoute builds and ships its own FRR sidecar image (`ghcr.io/azrtydxb/novaroute/novaroute-frr:<version>`). The agent communicates with FRR via VTY sockets and expects a specific minimum FRR version. Upgrading FRR independently of the agent is not supported -- always use the FRR image version shipped with the corresponding agent release.

---

## Upgrade Methods

### Helm Upgrade

If NovaRoute was deployed via Helm, upgrade using `helm upgrade`:

```bash
# Update the Helm repository
helm repo update novaroute

# Review the changes before applying
helm diff upgrade novaroute-agent novaroute/novaroute \
  --namespace nova-system \
  --values values.yaml

# Perform the upgrade
helm upgrade novaroute-agent novaroute/novaroute \
  --namespace nova-system \
  --values values.yaml \
  --version <target-version>
```

For the operator chart:

```bash
helm upgrade novaroute-operator novaroute/novaroute-operator \
  --namespace nova-system \
  --values operator-values.yaml \
  --version <target-version>
```

!!! warning
    Always upgrade the operator before upgrading agents. The operator must understand the new agent CRD schema before agents start reporting with it.

### ArgoCD Sync

If NovaRoute is managed by ArgoCD:

1. Update the `targetRevision` in your ArgoCD Application manifest to the new chart version.
2. Review the diff in the ArgoCD UI or CLI:

    ```bash
    argocd app diff novaroute-agent
    ```

3. Sync the application:

    ```bash
    argocd app sync novaroute-agent
    ```

4. Monitor the rollout:

    ```bash
    argocd app wait novaroute-agent --health --timeout 300
    ```

### Manual Upgrade (Raw Manifests)

If NovaRoute was deployed with raw YAML manifests:

1. Download the new manifests from the release:

    ```bash
    curl -LO https://github.com/azrtydxb/NovaRoute/releases/download/v<version>/deploy.yaml
    ```

2. Diff against your current deployment:

    ```bash
    kubectl diff -f deploy.yaml
    ```

3. Apply the updated manifests:

    ```bash
    kubectl apply -f deploy.yaml
    ```

4. Monitor the rollout:

    ```bash
    kubectl -n nova-system rollout status daemonset/novaroute-agent
    ```

---

## Rolling Update Behavior

NovaRoute runs as a DaemonSet, so Kubernetes performs rolling updates one node at a time by default.

### How It Works

The DaemonSet update strategy is configured as:

```yaml
updateStrategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 1
```

This means:

1. Kubernetes selects one node and terminates the old NovaRoute pod.
2. The agent receives a `SIGTERM` and begins graceful shutdown.
3. During the `terminationGracePeriodSeconds` (default: 60s), the agent:
    - Stops accepting new gRPC connections
    - Allows in-flight reconciliation to complete
    - FRR continues running until the pod is fully terminated
4. The old pod is removed and the new pod starts on that node.
5. The new agent connects to the new FRR sidecar and reconciles state.
6. Once the new pod is ready, Kubernetes moves to the next node.

### Impact on BGP Sessions

During a rolling update, each node experiences a brief routing disruption:

- **BGP sessions drop** when the FRR sidecar container is terminated. Peer routers detect the session loss via hold timer expiry (default: 90s) or BFD (sub-second detection if configured).
- **Routes are withdrawn** from the node being upgraded. Traffic is rerouted to other nodes if redundant paths exist.
- **Sessions re-establish** when the new FRR sidecar starts and the agent re-applies the intended configuration.

!!! tip
    To minimize disruption, configure BFD on all critical BGP sessions. BFD enables sub-second failure detection, allowing peer routers to reconverge quickly rather than waiting for the BGP hold timer to expire.

### Controlling the Rollout Speed

To upgrade more nodes in parallel (at the cost of more simultaneous disruption):

```bash
kubectl -n nova-system patch daemonset novaroute-agent \
  -p '{"spec":{"updateStrategy":{"rollingUpdate":{"maxUnavailable":3}}}}'
```

To pause a rollout mid-way, there is no native DaemonSet pause. Instead, use node selectors or partition-based updates with `OnDelete` strategy for finer control.

---

## Rollback Procedure

### Helm Rollback

```bash
# List release history
helm history novaroute-agent --namespace nova-system

# Roll back to the previous revision
helm rollback novaroute-agent <revision> --namespace nova-system

# Verify the rollback
kubectl -n nova-system rollout status daemonset/novaroute-agent
```

### Manual Rollback

If using raw manifests, re-apply the previous version:

```bash
kubectl apply -f deploy-previous-version.yaml
kubectl -n nova-system rollout status daemonset/novaroute-agent
```

### CRD Considerations During Rollback

!!! warning
    Helm does **not** roll back CRDs. If a new version introduced CRD schema changes, rolling back the operator does not revert the CRDs.

To handle CRD rollback manually:

1. Check if the new CRDs added required fields or removed existing ones.
2. If the old operator version is incompatible with the new CRDs, manually restore CRDs from your backup:

    ```bash
    kubectl apply -f novaroute-crds-backup.yaml
    ```

3. Verify that existing custom resources are still valid against the restored CRD schema:

    ```bash
    kubectl get novarouteclusters -A
    ```

### Post-Rollback Verification

After any rollback, verify the system is healthy:

```bash
# Check all pods are running
kubectl -n nova-system get pods -l app=novaroute-agent

# Verify BGP sessions re-established
kubectl -n nova-system exec <pod-name> -c frr -- vtysh -c "show bgp summary"

# Check agent status
novaroutectl status

# Verify metrics are being scraped
curl -s http://<node-ip>:9102/metrics | grep novaroute_frr_connected
```

---

## Breaking Changes

This section documents breaking changes by version. Review this before upgrading.

### v0.2.0

- **Initial release** -- no breaking changes from prior versions.

### Template for Future Versions

<!--
When documenting breaking changes for a new version, use this template:

### vX.Y.Z

- **Config format change:** Description of what changed in config.json and how to migrate.
- **CRD schema change:** Description of added/removed/modified fields in CRDs.
- **API change:** Description of gRPC API changes (new RPCs, changed request/response types).
- **Behavioral change:** Description of changed default values or changed runtime behavior.
- **Removed feature:** Description of deprecated features that were removed.
- **Migration steps:** Numbered list of required manual actions.
-->

---

## Troubleshooting Common Upgrade Issues

### CRD Conflicts

**Symptoms:** `helm upgrade` fails with errors about CRD ownership or conflicting field managers.

**Resolution:**

```bash
# Check who owns the CRD
kubectl get crd novarouteclusters.novaroute.io -o jsonpath='{.metadata.annotations}'

# If the CRD was manually applied and Helm cannot manage it,
# force-adopt the CRD (use with caution):
kubectl annotate crd novarouteclusters.novaroute.io \
  meta.helm.sh/release-name=novaroute-operator \
  meta.helm.sh/release-namespace=nova-system \
  --overwrite
kubectl label crd novarouteclusters.novaroute.io \
  app.kubernetes.io/managed-by=Helm \
  --overwrite
```

### RBAC Changes

**Symptoms:** The agent or operator logs show `Forbidden` errors after upgrade.

**Resolution:**

New versions may add RBAC rules for new API groups or resources. Verify that the ClusterRole and ClusterRoleBinding were updated:

```bash
# Check ClusterRole
kubectl get clusterrole novaroute-operator -o yaml

# Re-apply RBAC manifests if needed
kubectl apply -f deploy/rbac.yaml
```

If using Helm, RBAC resources are updated automatically during `helm upgrade`.

### Config Format Changes

**Symptoms:** The agent fails to start after upgrade with config parse errors.

**Resolution:**

1. Check the agent logs for the specific parse error:

    ```bash
    kubectl -n nova-system logs -l app=novaroute-agent -c novaroute-agent | head -50
    ```

2. Compare your `config.json` against the new version's example configuration in the release notes.

3. Common config migrations:
    - **Renamed fields:** Update field names in your ConfigMap or Helm values.
    - **New required fields:** Add missing fields with their default values.
    - **Changed types:** Convert field values to the new expected type (e.g., string to integer).

4. Update and re-apply the ConfigMap:

    ```bash
    kubectl -n nova-system edit configmap novaroute-config
    ```

5. Restart the DaemonSet to pick up the new config:

    ```bash
    kubectl -n nova-system rollout restart daemonset/novaroute-agent
    ```

### Image Pull Failures

**Symptoms:** Pods stuck in `ImagePullBackOff` after upgrade.

**Resolution:**

Verify the new image tag exists and is accessible:

```bash
# Check if the image exists in the registry
docker pull ghcr.io/azrtydxb/novaroute/novaroute-agent:<new-version>

# If using a private registry, verify pull secrets
kubectl -n nova-system get secret ghcr-pull-secret -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d
```

### FRR Version Mismatch

**Symptoms:** The agent connects to FRR but logs warnings about unsupported FRR version or missing VTY commands.

**Resolution:**

Ensure both the agent and FRR sidecar images are from the same release. Do not mix agent and FRR image versions independently:

```bash
# Check running image versions
kubectl -n nova-system get pods -l app=novaroute-agent \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{range .spec.containers[*]}{.image}{"\t"}{end}{"\n"}{end}'
```

### Stuck DaemonSet Rollout

**Symptoms:** `kubectl rollout status` hangs, some nodes have old pods, some have new pods.

**Resolution:**

```bash
# Check which pods are not updated
kubectl -n nova-system get pods -l app=novaroute-agent \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[0].image}{"\n"}{end}'

# Check for pods stuck in a bad state
kubectl -n nova-system get pods -l app=novaroute-agent --field-selector=status.phase!=Running

# If a pod is stuck, check events
kubectl -n nova-system describe pod <stuck-pod-name>

# As a last resort, delete the stuck pod to let the DaemonSet recreate it
kubectl -n nova-system delete pod <stuck-pod-name>
```
