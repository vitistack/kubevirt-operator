# KubevirtConfig Selection Logic

## Overview

The KubeVirt operator uses intelligent logic to determine which KubevirtConfig (and thus which cluster) should be used for a given Machine. This document explains the selection algorithm and its behavior in different scenarios.

## Selection Algorithm

The operator follows this decision tree:

```
┌─────────────────────────────────────────────────────────┐
│ 1. Does Machine have annotation                        │
│    "vitistack.io/kubevirt-config"?                     │
└─────────────────┬───────────────────────────────────────┘
                  │
          ┌───────┴────────┐
          │ YES            │ NO
          ▼                ▼
    ┌─────────┐    ┌──────────────────────────────────────┐
    │ Use it  │    │ 2. Is DEFAULT_KUBEVIRT_CONFIG set?   │
    │ (Done)  │    └────────────┬─────────────────────────┘
    └─────────┘                 │
                        ┌───────┴────────┐
                        │ YES            │ NO
                        ▼                ▼
                  ┌─────────┐    ┌──────────────────────────┐
                  │ Use it  │    │ 3. How many configs?     │
                  │(Set ann)│    └────────┬─────────────────┘
                  └─────────┘             │
                                  ┌───────┼───────┐
                                  │       │       │
                            ZERO  │  ONE  │  MULTIPLE
                                  ▼       ▼       ▼
                            ┌─────────┐ ┌─────────┐ ┌──────────────┐
                            │  ERROR  │ │ Use it  │ │ 4. Search    │
                            │         │ │(Set ann)│ │ for VM       │
                            └─────────┘ └─────────┘ └──────┬───────┘
                                                            │
                                                    ┌───────┼───────┐
                                                    │       │       │
                                              FOUND │  NOT  │ ERROR
                                                    │ FOUND │
                                                    ▼       ▼
                                            ┌─────────┐ ┌─────────┐
                                            │ Use it  │ │ ERROR   │
                                            │(Set ann)│ │ Ambig.  │
                                            └─────────┘ └─────────┘

Legend: (Set ann) = Set annotation on Machine
```

## Priority Levels

### Priority 1: Explicit Annotation

**Condition:** Machine has `vitistack.io/kubevirt-config` annotation

**Action:** Use that config

**Annotation Update:** No (already set)

**Example:**

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  annotations:
    vitistack.io/kubevirt-config: production-cluster
```

**Use Case:**

- Explicit cluster placement
- Override default behavior
- Production workloads requiring specific cluster

---

### Priority 2: Environment Default

**Condition:** `DEFAULT_KUBEVIRT_CONFIG` environment variable is set

**Action:** Use that config

**Annotation Update:** Yes (sets annotation)

**Example:**

```bash
export DEFAULT_KUBEVIRT_CONFIG=staging-cluster
```

**Use Case:**

- Organization-wide default cluster
- Environment-specific defaults (dev, staging, prod)
- Centralized cluster management

---

### Priority 3: Single Config Auto-Selection

**Condition:** Exactly ONE KubevirtConfig exists (cluster-scoped)

**Action:** Use that config automatically

**Annotation Update:** Yes (sets annotation)

**Example:**

```bash
$ kubectl get kubevirtconfig
NAME           SECRETNAMESPACE
cluster-main   kubevirt-secrets
```

**Use Case:**

- Single cluster deployments
- Simplified configuration
- Development/testing environments

---

### Priority 4: Multi-Config VM Search

**Condition:** MULTIPLE KubevirtConfigs exist, VM already exists somewhere

**Action:** Search all clusters for VM, use the one where it's found

**Annotation Update:** Yes (sets annotation when found)

**Search Process:**

1. Generate VM name: `vm-<machine-name>`
2. For each KubevirtConfig:
   - Get client for that cluster
   - Try to Get VirtualMachine with that name
   - If found, return that config
3. If not found in any cluster, return error

**Example:**

```bash
$ kubectl get kubevirtconfig
NAME            SECRETNAMESPACE
cluster-east    kubevirt-secrets
cluster-west    kubevirt-secrets

# VM vm-my-app already exists in cluster-west
# Machine my-app will automatically discover it
```

**Use Case:**

- Adopting existing VirtualMachines
- Multi-cluster migration
- Disaster recovery scenarios
- Reconciling after manual VM creation

---

## Error Scenarios

### Error 1: No Configs Available

**Condition:** Zero KubevirtConfigs exist (cluster-scoped)

**Error Message:**

```
machine default/my-vm has no KubevirtConfig reference and no KubevirtConfigs
are available
```

**Resolution:**

- Create at least one KubevirtConfig CRD (cluster-scoped)
- Check RBAC permissions
- Ensure the Secret exists in the namespace specified by `spec.secretNamespace`

---

### Error 2: Ambiguous Placement

**Condition:** Multiple configs exist, VM doesn't exist in any cluster

**Error Message:**

```
machine default/my-vm has no KubevirtConfig reference and 3 configs are available
- cannot determine placement. Please set annotation 'vitistack.io/kubevirt-config'
or DEFAULT_KUBEVIRT_CONFIG env var
```

**Resolution:**

- Add annotation: `vitistack.io/kubevirt-config: <cluster-name>`
- Or set DEFAULT_KUBEVIRT_CONFIG environment variable
- Or create VM manually in one cluster first

---

## Deployment Patterns

### Pattern 1: Single Cluster

**Setup:**

```yaml
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: main-cluster
  # No namespace - cluster-scoped
spec:
  name: main-cluster
  kubeconfigSecretRef: main-cluster-kubeconfig
  secretNamespace: kubevirt-secrets
```

**Machine Creation:**

```yaml
# No annotation needed!
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: app-server
```

**Behavior:**

- Automatic config selection (Priority 3)
- Zero configuration overhead
- Perfect for simple deployments

---

### Pattern 2: Multiple Clusters with Default

**Setup:**

```yaml
# Environment
DEFAULT_KUBEVIRT_CONFIG=production-east

# Configs (cluster-scoped)
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: production-east
spec:
  name: production-east
  kubeconfigSecretRef: production-east-kubeconfig
  secretNamespace: kubevirt-secrets
---
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: production-west
spec:
  name: production-west
  kubeconfigSecretRef: production-west-kubeconfig
  secretNamespace: kubevirt-secrets
```

**Machine Creation:**

```yaml
# Uses production-east by default
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: app-server
---
# Override for specific machine
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: west-app
  annotations:
    vitistack.io/kubevirt-config: production-west
```

**Behavior:**

- Most machines go to default cluster
- Easy to override for specific cases
- Centralized cluster management

---

### Pattern 3: Multiple Clusters with Adoption

**Setup:**

```yaml
# Multiple clusters (cluster-scoped)
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: cluster-a
spec:
  name: cluster-a
  kubeconfigSecretRef: cluster-a-kubeconfig
  secretNamespace: kubevirt-secrets
---
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: cluster-b
spec:
  name: cluster-b
  kubeconfigSecretRef: cluster-b-kubeconfig
  secretNamespace: kubevirt-secrets
```

**Scenario: Existing VMs**

```bash
# VM already exists in cluster-b
$ kubectl --context=cluster-b get vm -n default
NAME           AGE
vm-legacy-app  30d
```

**Machine Creation:**

```yaml
# Operator searches and finds VM in cluster-b
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: legacy-app # Matches vm-legacy-app
```

**Behavior:**

- Searches cluster-a and cluster-b
- Finds vm-legacy-app in cluster-b
- Sets annotation: `vitistack.io/kubevirt-config: cluster-b`
- Adopts existing VM

---

## Performance Considerations

### Search Overhead

When Priority 4 kicks in (multi-cluster search):

**Best Case:** VM found in first cluster

- Time: ~50-100ms (single API call)

**Worst Case:** VM not found, N clusters

- Time: ~50-100ms × N (serial search)
- Example: 5 clusters = ~500ms

**Optimization:** Set explicit annotation or default to avoid search

### Caching

The ClientManager caches clients:

- First access: ~200-500ms (create client, parse kubeconfig)
- Subsequent: ~50-100ms (cached client)

### Recommendations

1. **New Deployments:** Use DEFAULT_KUBEVIRT_CONFIG
2. **Single Cluster:** No configuration needed
3. **Multi-Cluster:** Explicit annotations for new VMs
4. **Migration:** Let search find existing VMs once, then annotation is set

---

## Future Enhancements

### 1. Default Flag on KubevirtConfig

```yaml
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: production-east
spec:
  default: true # This is the default cluster
```

**Benefit:** No environment variable needed

### 2. Load Balancing

```yaml
spec:
  placementStrategy: round-robin
  # or: least-loaded, cost-optimized, region-based
```

**Benefit:** Automatic distribution across clusters

### 3. Cluster Affinity

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  labels:
    region: us-east
spec:
  clusterSelector:
    matchLabels:
      region: us-east
```

**Benefit:** Rule-based placement

### 4. Cached Search Results

```go
// Cache VM location after first search
type vmLocationCache struct {
    machineName  string
    clusterName  string
    lastChecked  time.Time
}
```

**Benefit:** Avoid repeated searches

---

## Testing

### Test Case 1: Single Config

```go
func TestSingleConfig(t *testing.T) {
    mockMgr := clients.NewMockClientManager()
    mockMgr.AddMockedConfig(makeConfig("cluster-a"))
    mockMgr.AddMockedClient("cluster-a", fakeClient)

    machine := &Machine{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

    client, config, needsUpdate, err := mockMgr.GetOrCreateClientFromMachine(ctx, machine)

    assert.NoError(t, err)
    assert.Equal(t, "cluster-a", config)
    assert.True(t, needsUpdate)
}
```

### Test Case 2: Multiple Configs - Error

```go
func TestMultipleConfigsNoVM(t *testing.T) {
    mockMgr := clients.NewMockClientManager()
    mockMgr.AddMockedConfig(makeConfig("cluster-a"))
    mockMgr.AddMockedConfig(makeConfig("cluster-b"))

    machine := &Machine{ObjectMeta: metav1.ObjectMeta{Name: "test"}}

    _, _, _, err := mockMgr.GetOrCreateClientFromMachine(ctx, machine)

    assert.Error(t, err)
    assert.Contains(t, err.Error(), "cannot determine placement")
}
```

### Test Case 3: Explicit Annotation

```go
func TestExplicitAnnotation(t *testing.T) {
    mockMgr := clients.NewMockClientManager()
    mockMgr.AddMockedClient("cluster-b", fakeClient)

    machine := &Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name: "test",
            Annotations: map[string]string{
                "vitistack.io/kubevirt-config": "cluster-b",
            },
        },
    }

    client, config, needsUpdate, err := mockMgr.GetOrCreateClientFromMachine(ctx, machine)

    assert.NoError(t, err)
    assert.Equal(t, "cluster-b", config)
    assert.False(t, needsUpdate) // Already set
}
```

---

## Summary

The config selection logic provides:

✅ **Simplicity:** Single cluster = zero configuration  
✅ **Flexibility:** Multiple patterns for different use cases  
✅ **Adoption:** Can discover existing VMs across clusters  
✅ **Safety:** Explicit placement when ambiguous  
✅ **Performance:** Client caching, minimal overhead  
✅ **Visibility:** Annotations show final placement

The algorithm balances automation with safety, ensuring VMs are placed correctly without requiring excessive configuration.
