# Multi-Cluster KubeVirt Operator Changes

## Overview

This document describes the architectural changes made to enable the KubeVirt operator to run on a supervisor cluster while managing VirtualMachines across multiple remote KubeVirt clusters.

## Architecture

### Before

- Operator ran in the same cluster as the VirtualMachines
- Single Kubernetes client used for all operations
- Machine CRs and VirtualMachines colocated in the same cluster

### After

- Operator runs in a **supervisor cluster**
- Machine CRs stored in supervisor cluster
- VirtualMachines created in **remote KubeVirt clusters**
- Multiple Kubernetes clients managed dynamically
- KubevirtConfig CRDs define remote cluster connections

## Key Components

### 1. ClientManager Interface & KubevirtClientManager (`pkg/clients/`)

**Interface-based design** for managing multiple Kubernetes clients for different KubeVirt clusters.

**ClientManager Interface (`interface.go`):**
Defines the contract for all client manager implementations:

- `GetClientForConfig()` - Get or create a client for a specific config
- `GetOrCreateClientFromMachine()` - Extract config reference from Machine and return client
- `ListKubevirtConfigs()` - List all available KubeVirt cluster configs
- `ValidateConnection()` - Test connectivity to a cluster
- `HealthCheck()` - Check health of all cached clients
- And more...

**KubevirtClientManager (Production Implementation):**

- Fetches KubevirtConfig CRDs from a configured namespace
- Creates and caches clients from kubeconfig secrets
- Provides client lookup by config name
- Validates connections to remote clusters
- Supports health checks for all managed clusters

**MockClientManager (Testing Implementation):**

- Allows injecting pre-configured clients for testing
- Tracks method calls for verification
- Supports custom behavior functions
- Simulates errors for edge case testing

**Key Methods:**

- `GetClientForConfig(ctx, kubevirtConfigName)` - Get or create a client for a specific config
- `GetOrCreateClientFromMachine(ctx, machine)` - Extract config reference from Machine and return client
- `ListKubevirtConfigs(ctx)` - List all available KubeVirt cluster configs
- `ValidateConnection(ctx, kubevirtConfigName)` - Test connectivity to a cluster
- `HealthCheck(ctx)` - Check health of all cached clients

**Benefits of Interface-Based Design:**

- **Testability**: Easy to mock for unit tests
- **Flexibility**: Can swap implementations
- **Dependency Injection**: Cleaner separation of concerns
- **Extensibility**: Easy to add new implementations

### 2. Updated VMManager (`internal/machine/vm/vm_manager.go`)

Modified to support dual-client architecture:

**Changes:**

- Now has both `supervisorClient` (for Machine CRs, NetworkConfiguration) and `remoteClient` (for VMs)
- `SetRemoteClient(client)` method to set the target KubeVirt cluster client
- All VM operations use `remoteClient`
- All NetworkConfiguration operations use `supervisorClient`

### 3. Updated MachineReconciler (`controllers/v1alpha1/machine_controller.go`)

Enhanced to orchestrate multi-cluster operations:

**Changes:**

- Added `KubevirtClientMgr` field (type `clients.ClientManager` interface)
- Reconcile flow now:
  1. Fetches Machine from supervisor cluster
  2. Gets remote client from ClientManager
  3. Sets remote client in VMManager
  4. Creates/updates VMs in remote cluster
  5. Fetches VM/VMI status from remote cluster
  6. Updates Machine status in supervisor cluster

**New Methods:**

- `recordKubevirtConfigFailure()` - Handle errors when getting remote clients
- Updated `ensureVirtualMachine()` to accept remote client parameter
- Updated `getVMI()` to fetch from remote cluster
- Updated `handleDeletion()` to clean up VMs in remote cluster

**Interface Benefits:**
The reconciler depends on the `ClientManager` interface rather than the concrete implementation, making it:

- Easier to test with mock implementations
- More flexible for different deployment scenarios
- Better separated from client management concerns

### 4. Environment Variables & Configuration

**New Constants (`internal/consts/app_consts.go`):**

- `DEFAULT_KUBEVIRT_CONFIG` - Optional default config name if Machine doesn't specify one

**Configuration in `cmd/main.go`:**

- Initializes KubevirtClientManager
- Passes manager to reconciler constructor

> **Note:** KubevirtConfig is cluster-scoped. Each KubevirtConfig specifies its own `spec.secretNamespace` where the kubeconfig secret is stored.

### 5. RBAC Updates

**New Permissions:**

```go
// +kubebuilder:rbac:groups=vitistack.io,resources=kubevirtconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list
```

These allow the operator to:

- Read KubevirtConfig CRDs
- Read kubeconfig Secrets referenced by KubevirtConfigs
- Read CRD definitions for version compatibility checks

## KubevirtConfig CRD

KubevirtConfig is a **cluster-scoped** resource. Each KubevirtConfig specifies its own `secretNamespace` where the kubeconfig secret is stored.

Expected structure (from `hack/vitistack-crds/vitistack.io_kubevirtconfigs.yaml`):

```yaml
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: cluster-east
  # No namespace - cluster-scoped resource
spec:
  name: cluster-east
  kubeconfigSecretRef: cluster-east-kubeconfig
  secretNamespace: kubevirt-secrets # Namespace where the secret is stored
status:
  phase: Ready
  status: Connected
```

The referenced Secret should be in the namespace specified by `secretNamespace`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: cluster-east-kubeconfig
  namespace: kubevirt-secrets # Must match spec.secretNamespace in KubevirtConfig
type: Opaque
data:
  kubeconfig: <base64-encoded-kubeconfig>
```

## Machine Configuration

Until the Machine CRD is updated to include a `spec.kubevirtConfigRef` or `status.kubevirtConfigRef` field, the operator uses annotations:

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: my-machine
  annotations:
    vitistack.io/kubevirt-config: cluster-east # Optional, see below
spec:
  instanceType: medium
  # ... other spec fields
```

### Automatic KubevirtConfig Assignment

The operator intelligently assigns a KubevirtConfig to Machines using this logic:

1. **Machine Annotation** (explicit): If the Machine has `vitistack.io/kubevirt-config` annotation, use that config
2. **Default Config** (environment): If `DEFAULT_KUBEVIRT_CONFIG` environment variable is set, use that config
3. **Single Config** (automatic): If exactly ONE KubevirtConfig exists, automatically use it
4. **Multiple Configs** (search): If MULTIPLE KubevirtConfigs exist:
   - Search all clusters for the VirtualMachine (vm-<machine-name>)
   - If found in one cluster, use that config
   - If not found or ambiguous, return error requiring explicit configuration

When the operator automatically selects a config (priority 2, 3, or 4), it will:

- Assign that config to the Machine
- Add the `vitistack.io/kubevirt-config` annotation automatically
- Log the assignment for visibility

#### Single Cluster Scenario

If you have only one KubevirtConfig, Machines can be created **without specifying a config**:

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: my-machine
  # No annotation needed with single cluster!
spec:
  instanceType: medium
```

The operator will log:

```
Automatically assigned KubevirtConfig to machine machine=my-machine kubevirtConfig=cluster-east
```

#### Multiple Cluster Scenario

With multiple KubevirtConfigs, you have two options:

**Option 1: Explicit annotation** (recommended for new VMs)

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: my-machine
  annotations:
    vitistack.io/kubevirt-config: cluster-west
spec:
  instanceType: medium
```

**Option 2: Set default** (for all new VMs)

```yaml
env:
  - name: DEFAULT_KUBEVIRT_CONFIG
    value: "cluster-east"
```

**Option 3: Existing VM search** (for VMs already created)

- Operator searches all clusters for existing VM
- Automatically sets annotation when found
- Useful for adopting existing VirtualMachines

**TODO:** Update the Machine CRD to include proper KubevirtConfig reference fields.

## Data Flow

### Machine Creation

1. User creates Machine CR in supervisor cluster
2. Reconciler reads Machine
3. Reconciler gets remote client from KubevirtClientManager based on annotation
4. Reconciler sets remote client in VMManager
5. VMManager creates VirtualMachine in **remote cluster**
6. VMManager creates NetworkConfiguration in **supervisor cluster**
7. Reconciler updates Machine status in **supervisor cluster**

### Machine Status Updates

1. Reconciler fetches VirtualMachine from **remote cluster**
2. Reconciler fetches VMI from **remote cluster**
3. Status manager evaluates state
4. Machine status updated in **supervisor cluster**

### Machine Deletion

1. Reconciler fetches Machine from supervisor cluster
2. Reconciler gets remote client
3. VMManager deletes VirtualMachine from **remote cluster**
4. VMManager deletes NetworkConfiguration from **supervisor cluster**
5. StorageManager deletes PVCs from supervisor cluster (if applicable)
6. Reconciler removes finalizer from Machine in supervisor cluster

## Deployment Configuration

### Environment Variables

Set these when deploying the operator:

```yaml
env:
  - name: DEFAULT_KUBEVIRT_CONFIG
    value: "default-cluster" # Optional
```

> **Note:** The `KUBEVIRT_CONFIGS_NAMESPACE` environment variable is no longer needed. Each KubevirtConfig specifies its own `spec.secretNamespace`.

### Initialization Checks

The operator performs several checks on startup:

1. **Supervisor Cluster CRDs**: Validates that Machine and KubevirtConfig CRDs are installed
2. **KubevirtConfig Availability**: Ensures at least one KubevirtConfig exists
3. **Remote Cluster Connectivity**: Tests connection to each remote cluster
4. **KubeVirt Components**: Verifies KubeVirt is installed and running on remote clusters
5. **CRD Version Compatibility**: Checks that CRD versions match between supervisor and remote clusters (logs warnings for mismatches)

**CRD Version Compatibility Check:**

The operator automatically validates CRD schemas and versions during initialization to help catch compatibility issues early:

**Supervisor Cluster (vitistack.io CRDs):**

- Fetches complete CRD schemas for machines, networkconfigurations, and kubevirtconfigs
- Validates that expected properties exist in the CRD schemas:
  - `machines.vitistack.io`: instanceType, kubevirtConfigRef, phase, conditions
  - `networkconfigurations.vitistack.io`: networks, phase
  - `kubevirtconfigs.vitistack.io`: kubeconfigSecretRef, phase
- Logs warnings if expected fields are missing from the schema
- **Note:** KubeVirt is NOT installed on the supervisor cluster

**Remote KubeVirt Clusters (kubevirt.io CRDs):**

- Compares `kubevirt.io` CRD versions (virtualmachines, virtualmachineinstances) across all remote clusters
- Uses first remote cluster as baseline
- Logs warnings if versions differ between remote clusters
- **Note:** These CRDs only exist on remote clusters, not on supervisor

**Why this approach?**

- Supervisor cluster runs the operator and stores Machine CRs (vitistack.io CRDs)
- Remote clusters run KubeVirt and host VirtualMachines (kubevirt.io CRDs)
- Schema validation ensures the operator can use all expected fields
- Version consistency across remote clusters prevents unexpected behavior when moving workloads
- Catches CRD mismatches before they cause runtime errors

**Log output:**

- **Matching schemas/versions**: Logged as info (✅)
- **Mismatched versions**: Logged as warning (⚠️) with details
- **Missing CRD fields**: Logged as warning (⚠️) with field names
- **Missing CRDs**: Logged as warning (⚠️) if a CRD doesn't exist

These warnings are informational and don't block operator startup, but they help identify potential compatibility issues.

### Namespace Setup

Create a namespace for kubeconfig secrets:

```bash
kubectl create namespace kubevirt-secrets
```

### Create KubevirtConfig and Secret

```yaml
---
apiVersion: v1
kind: Secret
metadata:
  name: cluster-east-kubeconfig
  namespace: kubevirt-secrets # Namespace for secrets
type: Opaque
stringData:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - cluster:
        server: https://cluster-east.example.com:6443
        certificate-authority-data: <CA_DATA>
      name: cluster-east
    contexts:
    - context:
        cluster: cluster-east
        user: kubevirt-operator
      name: cluster-east
    current-context: cluster-east
    users:
    - name: kubevirt-operator
      user:
        token: <SERVICE_ACCOUNT_TOKEN>
---
apiVersion: vitistack.io/v1alpha1
kind: KubevirtConfig
metadata:
  name: cluster-east
  # No namespace - cluster-scoped resource
spec:
  name: cluster-east
  kubeconfigSecretRef: cluster-east-kubeconfig
  secretNamespace: kubevirt-secrets # References the secret namespace
```

## Testing

### Unit Tests

The interface-based design makes unit testing straightforward:

```go
func TestMachineReconciler_CreateVM(t *testing.T) {
    // Setup mock client manager
    mockMgr := clients.NewMockClientManager()
    fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).Build()
    mockMgr.AddMockedClient("test-cluster", fakeRemoteClient)

    // Create reconciler with mock
    reconciler := v1alpha1.NewMachineReconciler(
        fake.NewClientBuilder().WithScheme(scheme).Build(),
        scheme,
        mockMgr,
    )

    // Create test machine
    machine := &vitistackv1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name: "test-machine",
            Namespace: "default",
            Annotations: map[string]string{
                "vitistack.io/kubevirt-config": "test-cluster",
            },
        },
        Spec: vitistackv1alpha1.MachineSpec{
            InstanceType: "medium",
        },
    }

    // Test reconciliation
    _, err := reconciler.Reconcile(ctx, reconcile.Request{
        NamespacedName: types.NamespacedName{
            Name: "test-machine",
            Namespace: "default",
        },
    })

    // Verify
    assert.NoError(t, err)
    assert.Equal(t, 1, mockMgr.GetOrCreateFromMachineCalls)
}
```

**Key Areas to Test:**

- Client manager client creation and caching
- Machine annotation parsing for kubevirt-config
- Remote client selection logic
- Error handling when remote cluster unavailable
- VM creation in remote cluster vs Machine updates in supervisor cluster

### Integration Tests

Scenarios:

1. Create Machine with valid kubevirt-config annotation → VM created in remote cluster
2. Create Machine without annotation → Falls back to default config
3. Update VM in remote cluster → Machine status synced in supervisor
4. Delete Machine → VM cleaned up in remote cluster
5. Invalid kubevirt-config → Machine marked as Failed

## Limitations & Future Work

### Current Limitations

1. **No CRD Field for KubevirtConfig Reference**

   - Using annotations as a workaround
   - TODO: Update Machine CRD to include proper reference field

2. **Polling-Based Status Updates**

   - Status updates happen on reconciliation intervals (30 seconds via RequeueAfter)
   - This is simpler and more reliable than managing informers for multiple remote clusters
   - Adequate for most use cases
   - Future enhancement: Implement event-driven watches for real-time updates

3. **No Multi-Namespace Support**

   - All KubevirtConfigs must be in the same namespace
   - Could be extended to support cluster-wide configs

4. **PVC Handling**
   - Currently assumes PVCs are in supervisor cluster
   - May need to support PVCs in remote clusters

### Future Enhancements

1. **Event-Driven VM Status Synchronizer**

   **Current Approach:** Polling-based reconciliation with `RequeueAfter` (30s interval)

   - Simple and reliable
   - Works well for most use cases
   - No additional complexity

   **Future Enhancement:** Event-driven watches

   - Watch VMs/VMIs in remote clusters for real-time updates
   - Requires managing informers for multiple remote clusters
   - More complex but provides instant reconciliation on VM changes
   - Trade-off: Complexity vs. responsiveness

2. **Client Refresh/Rotation**

   - Handle kubeconfig secret updates
   - Automatic client invalidation on connection errors
   - Reconnection logic

3. **Machine CRD Updates**
   - Add `spec.kubevirtConfigRef` field
   - Add `status.kubevirtConfigRef` field
   - Migration path from annotations

## Migration Guide

For existing deployments migrating to this multi-cluster architecture:

1. **Deploy KubevirtConfig CRDs**

   ```bash
   kubectl apply -f hack/vitistack-crds/vitistack.io_kubevirtconfigs.yaml
   ```

2. **Create namespace for secrets**

   ```bash
   kubectl create namespace kubevirt-secrets
   ```

3. **Create KubevirtConfig for existing cluster**

   - Extract current kubeconfig
   - Create Secret with kubeconfig in the secrets namespace
   - Create cluster-scoped KubevirtConfig with `secretNamespace` pointing to the secrets namespace

4. **Update operator deployment**

   - Add DEFAULT_KUBEVIRT_CONFIG env var (optional)
   - Update RBAC to include new permissions

5. **Update existing Machines**

   - Add annotation `vitistack.io/kubevirt-config: <config-name>`
   - Or rely on DEFAULT_KUBEVIRT_CONFIG

6. **Verify**
   - Check operator logs for client initialization
   - Verify VMs still accessible
   - Test creating new Machines

## Troubleshooting

### Common Issues

**1. "Failed to get KubeVirt client for machine"**

- Check KubevirtConfig exists in configured namespace
- Verify kubeconfigSecretRef points to valid Secret
- Check Secret contains 'kubeconfig' key
- Validate kubeconfig is valid and accessible

**2. "Machine has no KubevirtConfig reference and X configs are available - cannot determine placement"**

- This error occurs when multiple KubevirtConfigs exist and the VM doesn't exist in any cluster yet
- The operator cannot automatically choose which cluster to place the VM in
- **Solutions:**
  - Add annotation to Machine: `vitistack.io/kubevirt-config: <name>`
  - Set DEFAULT_KUBEVIRT_CONFIG environment variable
  - If VM already exists in a cluster, the operator will find it automatically

**3. "Machine has no KubevirtConfig reference and no KubevirtConfigs are available"**

- This means no KubevirtConfig CRDs exist in the configured namespace
- Create at least one KubevirtConfig CRD (see Configuration section)
- With a single config, Machines will automatically use it

**3. "Failed to create VirtualMachine in remote cluster"**

- Check remote cluster is accessible
- Verify kubeconfig has necessary permissions
- Check namespace exists in remote cluster
- Verify KubeVirt is installed in remote cluster

**4. "Failed to get VirtualMachine from remote cluster"**

- VM may not exist yet
- Remote cluster may be unreachable
- Check kubeconfig permissions

### Debug Commands

```bash
# List KubevirtConfigs (cluster-scoped, no namespace needed)
kubectl get kubevirtconfigs

# Check KubevirtConfig details
kubectl describe kubevirtconfig <name>

# Verify Secret (check the secretNamespace from the KubevirtConfig)
kubectl get secret <secret-name> -n <secret-namespace> -o yaml

# Check operator logs
kubectl logs -n <operator-namespace> <operator-pod> -f

# Check Machine status
kubectl get machine <name> -o yaml

# Check VM in remote cluster (using remote kubeconfig)
kubectl --kubeconfig=<remote-kubeconfig> get vm -n <namespace>
```

## References

- KubevirtConfig CRD: `hack/vitistack-crds/vitistack.io_kubevirtconfigs.yaml`
- Machine CRD: External vitistack/crds repository
- KubeVirt Documentation: https://kubevirt.io/
