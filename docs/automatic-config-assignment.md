# Automatic KubevirtConfig Assignment

## Overview

The KubeVirt operator now automatically assigns KubevirtConfig to Machines that don't have an explicit configuration reference. This eliminates the need to manually annotate every Machine and provides a better user experience.

## Behavior

### Config Selection Logic

When a Machine is reconciled, the operator determines which KubevirtConfig to use with the following logic:

1. **Machine Annotation** (explicit)

   - Check `vitistack.io/kubevirt-config` annotation on the Machine
   - If present, use that specific config
   - **Highest priority** - always honored

2. **Default Config** (environment)

   - Check `DEFAULT_KUBEVIRT_CONFIG` environment variable
   - If set, use that as the default config for all machines
   - **Applies to all Machines without explicit annotation**

3. **Single Config** (automatic)

   - Query all KubevirtConfigs in the configured namespace
   - If exactly **ONE** config exists, automatically use it
   - **Safe automatic selection** - no ambiguity

4. **Multiple Configs** (search)
   - If **MULTIPLE** configs exist without explicit reference:
     - Search all clusters for the VirtualMachine (vm-<machine-name>)
     - If found in exactly one cluster, use that config
     - If not found in any cluster, return error (ambiguous placement)
   - **Useful for adopting existing VMs** across clusters

### Automatic Annotation

When the operator automatically selects a config (priority 2 or 3), it will:

1. Assign that config to the Machine
2. Add/update the `vitistack.io/kubevirt-config` annotation
3. Log the automatic assignment
4. Return a flag indicating the annotation was updated

This ensures that the config selection is persisted and visible to users.

## Usage Examples

### Example 1: Explicit Config

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: production-vm
  annotations:
    vitistack.io/kubevirt-config: production-cluster
spec:
  instanceType: large
```

**Result:** Uses `production-cluster` (priority 1)

### Example 2: No Annotation, With Default

```yaml
# Environment variable set: DEFAULT_KUBEVIRT_CONFIG=staging-cluster
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: test-vm
spec:
  instanceType: small
```

**Result:**

- Uses `staging-cluster` (priority 2)
- Annotation `vitistack.io/kubevirt-config: staging-cluster` is automatically added
- Operator logs: "Automatically assigned KubevirtConfig to machine machine=test-vm kubevirtConfig=staging-cluster"

### Example 3: Single Config, No Annotation

```yaml
# No DEFAULT_KUBEVIRT_CONFIG set
# Available configs: cluster-east (only one)
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: dynamic-vm
spec:
  instanceType: medium
```

**Result:**

- Uses the single available config `cluster-east` (priority 3)
- Annotation `vitistack.io/kubevirt-config: cluster-east` is automatically added
- Operator logs: "Automatically assigned KubevirtConfig to machine machine=dynamic-vm kubevirtConfig=cluster-east"

### Example 3b: Multiple Configs, Existing VM

```yaml
# No DEFAULT_KUBEVIRT_CONFIG set
# Available configs: cluster-east, cluster-west
# VM vm-dynamic-vm already exists in cluster-west
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: dynamic-vm
spec:
  instanceType: medium
```

**Result:**

- Searches both clusters for vm-dynamic-vm
- Finds it in cluster-west
- Uses `cluster-west` (priority 4)
- Annotation `vitistack.io/kubevirt-config: cluster-west` is automatically added
- Operator logs: "Automatically assigned KubevirtConfig to machine machine=dynamic-vm kubevirtConfig=cluster-west"

### Example 4: Multiple Configs, New VM

```yaml
# No DEFAULT_KUBEVIRT_CONFIG set
# Available configs: cluster-east, cluster-west
# VM vm-new-vm does NOT exist in any cluster yet
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: new-vm
spec:
  instanceType: small
```

**Result:**

- Searches both clusters for vm-new-vm
- Not found in any cluster (new VM)
- Error: "machine default/new-vm has no KubevirtConfig reference and 2 configs are available - cannot determine placement. Please set annotation 'vitistack.io/kubevirt-config' or DEFAULT_KUBEVIRT_CONFIG env var"
- Machine enters error state
- Event recorded on Machine object

**Solution:** Add annotation or set DEFAULT_KUBEVIRT_CONFIG

### Example 5: No Configs Available

```yaml
# No KubevirtConfigs exist
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: broken-vm
spec:
  instanceType: small
```

**Result:**

- Error: "machine default/broken-vm has no KubevirtConfig reference and no KubevirtConfigs are available"
- Machine enters error state
- Event recorded on Machine object

## Configuration

### Setting a Default Config

Set the environment variable in the operator deployment:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kubevirt-operator
spec:
  template:
    spec:
      containers:
        - name: manager
          env:
            - name: DEFAULT_KUBEVIRT_CONFIG
              value: "production-cluster"
```

Or using Viper configuration file:

```yaml
# config.yaml
DEFAULT_KUBEVIRT_CONFIG: production-cluster
```

### Creating KubevirtConfigs

Ensure at least one KubevirtConfig exists (cluster-scoped resource):

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

## Benefits

### 1. Simplified Machine Creation

**Before:**

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: my-vm
  annotations:
    vitistack.io/kubevirt-config: some-cluster # Required!
spec:
  instanceType: medium
```

**After:**

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: my-vm
  # No annotation needed!
spec:
  instanceType: medium
```

### 2. Flexible Deployment Models

**Single Cluster:**

- Create one KubevirtConfig
- All Machines automatically use it
- No annotation management needed

**Multiple Clusters with Default:**

- Set `DEFAULT_KUBEVIRT_CONFIG` to preferred cluster
- Machines use default unless explicitly overridden
- Easy to move workloads to specific clusters when needed

**Multiple Clusters without Default:**

- Machines distributed to first available cluster
- Explicit annotations only when cluster placement matters
- Balanced approach between automation and control

### 3. Better User Experience

- Less boilerplate in Machine definitions
- Fewer manual steps to create a Machine
- Automatic config selection is logged for visibility
- Config assignment is persisted in annotations for debugging

### 4. Migration Path

Existing Machines with annotations continue to work as before:

- Annotation takes highest priority
- No breaking changes
- Gradual migration possible

## Implementation Details

### Interface Change

```go
// Before
GetOrCreateClientFromMachine(ctx context.Context, machine *Machine) (client.Client, string, error)

// After
GetOrCreateClientFromMachine(ctx context.Context, machine *Machine) (client.Client, string, bool, error)
//                                                                                          ^^^^
//                                                                                 needsAnnotationUpdate
```

### Controller Logic

```go
// Get client with automatic config selection
remoteClient, kubevirtConfigName, needsAnnotationUpdate, err := r.KubevirtClientMgr.GetOrCreateClientFromMachine(ctx, machine)
if err != nil {
    return ctrl.Result{}, err
}

// Update annotation if config was automatically selected
if needsAnnotationUpdate {
    if machine.Annotations == nil {
        machine.Annotations = make(map[string]string)
    }
    machine.Annotations["vitistack.io/kubevirt-config"] = kubevirtConfigName
    if err := r.Update(ctx, machine); err != nil {
        logger.Error(err, "Failed to update machine annotations")
    } else {
        logger.Info("Automatically assigned KubevirtConfig to machine",
            "machine", machine.Name,
            "kubevirtConfig", kubevirtConfigName)
    }
}
```

### Client Manager Logic

```go
func (m *KubevirtClientManager) GetOrCreateClientFromMachine(ctx context.Context, machine *Machine) (client.Client, string, bool, error) {
    kubevirtConfigName := ""
    needsAnnotationUpdate := false

    // Priority 1: Check annotation
    if machine.Annotations != nil {
        kubevirtConfigName = machine.Annotations["vitistack.io/kubevirt-config"]
    }

    if kubevirtConfigName == "" {
        // Priority 2: Check default
        kubevirtConfigName = viper.GetString("DEFAULT_KUBEVIRT_CONFIG")

        if kubevirtConfigName == "" {
            configs, err := m.ListKubevirtConfigs(ctx)
            if err != nil {
                return nil, "", false, err
            }
            if len(configs) == 0 {
                return nil, "", false, fmt.Errorf("no KubevirtConfigs available")
            }

            // Priority 3: Single config
            if len(configs) == 1 {
                kubevirtConfigName = configs[0].Name
            } else {
                // Priority 4: Multiple configs - search for VM
                kubevirtConfigName, err = m.findVMInClusters(ctx, machine, configs)
                if err != nil {
                    return nil, "", false, err
                }
                if kubevirtConfigName == "" {
                    // VM not found - cannot determine placement
                    return nil, "", false, fmt.Errorf("ambiguous cluster placement")
                }
            }
        }

        // Config was automatically determined
        needsAnnotationUpdate = true
    }

    client, err := m.GetClientForConfig(ctx, kubevirtConfigName)
    return client, kubevirtConfigName, needsAnnotationUpdate, err
}

// findVMInClusters searches for the VM across all clusters
func (m *KubevirtClientManager) findVMInClusters(ctx context.Context, machine *Machine, configs []KubevirtConfig) (string, error) {
    vmName := fmt.Sprintf("vm-%s", machine.Name)

    for _, config := range configs {
        remoteClient, err := m.GetClientForConfig(ctx, config.Name)
        if err != nil {
            continue // Skip unreachable clusters
        }

        vm := &kubevirtv1.VirtualMachine{}
        err = remoteClient.Get(ctx, client.ObjectKey{
            Namespace: machine.Namespace,
            Name:      vmName,
        }, vm)

        if err == nil {
            // Found the VM!
            return config.Name, nil
        }
    }

    // VM not found in any cluster
    return "", nil
}
```

## Testing

### Unit Tests with Mock

```go
func TestAutomaticConfigSelection(t *testing.T) {
    mockMgr := clients.NewMockClientManager()

    // Add available configs
    mockMgr.AddMockedConfig(vitistackv1alpha1.KubevirtConfig{
        ObjectMeta: metav1.ObjectMeta{Name: "cluster-a"},
    })
    mockMgr.AddMockedClient("cluster-a", fakeClient)

    // Machine without annotation
    machine := &vitistackv1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name: "test-vm",
        },
    }

    client, configName, needsUpdate, err := mockMgr.GetOrCreateClientFromMachine(ctx, machine)

    assert.NoError(t, err)
    assert.NotNil(t, client)
    assert.Equal(t, "cluster-a", configName)
    assert.True(t, needsUpdate, "Should indicate annotation needs updating")
}
```

### Integration Testing

1. Create KubevirtConfigs without setting DEFAULT_KUBEVIRT_CONFIG
2. Create Machine without annotation
3. Verify Machine gets annotation added
4. Verify VM created in correct cluster

## Migration Guide

### From Manual Annotation

If you have existing Machines with annotations:

1. **No action required** - annotations take highest priority
2. Machines continue to work as before
3. Can gradually remove annotations if desired

### To Use Default Config

1. Set `DEFAULT_KUBEVIRT_CONFIG` environment variable
2. Remove annotations from new Machines
3. Existing machines keep working with their annotations

### To Use First Available

1. Don't set `DEFAULT_KUBEVIRT_CONFIG`
2. Don't add annotations to Machines
3. Operator automatically uses first available config

## Troubleshooting

### Machine Keeps Using Wrong Cluster

**Problem:** Machine uses different cluster than expected

**Solutions:**

1. Check if annotation exists: `kubectl get machine <name> -o jsonpath='{.metadata.annotations}'`
2. Check if DEFAULT_KUBEVIRT_CONFIG is set: `kubectl get deployment kubevirt-operator -o yaml | grep DEFAULT_KUBEVIRT_CONFIG`
3. Check available configs: `kubectl get kubevirtconfig` (cluster-scoped, no namespace needed)
4. Add explicit annotation to override automatic selection

### No KubevirtConfigs Available Error

**Problem:** "machine has no KubevirtConfig reference and no KubevirtConfigs are available"

**Solutions:**

1. Create at least one KubevirtConfig (cluster-scoped)
2. Check operator has RBAC permissions to list KubevirtConfigs
3. Verify KubevirtConfig has valid `spec.secretNamespace` pointing to a namespace with the kubeconfig secret

### Annotation Not Being Added

**Problem:** Machine works but annotation not added

**Solutions:**

1. Check operator logs for update errors
2. Verify operator has RBAC permission to update Machines
3. Check if annotation already exists (won't overwrite existing)

## Future Improvements

### 1. CRD Field

Replace annotation with proper CRD field:

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
spec:
  kubevirtConfigRef:
    name: cluster-east
```

### 2. Load Balancing

Distribute Machines across multiple clusters:

```go
// Round-robin or weighted distribution
kubevirtConfigName = m.selectConfigWithLoadBalancing(ctx)
```

### 3. Cluster Affinity

Support cluster selection based on labels:

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

### 4. Health-Based Selection

Prefer healthy clusters:

```go
// Skip unhealthy clusters when auto-selecting
configs := m.getHealthyConfigs(ctx)
```

## Conclusion

Automatic KubevirtConfig assignment provides a better user experience while maintaining flexibility. Users can:

- Create Machines without annotations (simplest)
- Set a default config for consistency
- Use explicit annotations for control

The feature is backward compatible and provides a clear migration path for existing deployments.
