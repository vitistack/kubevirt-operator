# Namespace Management in Multi-Cluster Architecture

## Overview

The KubeVirt Operator follows a multi-cluster architecture where:

- **Supervisor Cluster**: Where the Machine CRDs are created and managed
- **Remote KubeVirt Cluster**: Where the actual VirtualMachines, PVCs, and other resources are deployed

## Namespace Behavior

### Machine Namespace Propagation

When you create a Machine resource on the supervisor cluster, the operator ensures that **the same namespace exists on the remote KubeVirt cluster**.

```yaml
# Created on SUPERVISOR cluster
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: my-vm
  namespace: production # ← This namespace
spec:
  instanceType: small
```

The operator will:

1. Check if the `production` namespace exists on the remote KubeVirt cluster
2. If it doesn't exist, create it with the label `managed-by: vitistack-kubevirt-operator`
3. Create all resources (VirtualMachine, PVCs) in the `production` namespace on the remote cluster

### Why This Matters

This ensures **consistent namespace naming across clusters**, making it easier to:

- Organize resources by environment (dev, staging, production)
- Apply network policies and RBAC consistently
- Identify which resources belong together across clusters

## Examples

### Default Namespace

```yaml
# Machine in default namespace (exists on all clusters)
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: test-vm
  namespace: default # No namespace creation needed
spec:
  instanceType: small
```

### Custom Namespace

```yaml
# First, create namespace on supervisor cluster
apiVersion: v1
kind: Namespace
metadata:
  name: my-app
  labels:
    environment: production
---
# Then create Machine in that namespace
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: app-vm
  namespace: my-app # Automatically created on remote cluster
spec:
  instanceType: medium
```

### Multiple Machines in Same Namespace

```yaml
# Multiple machines can share the same namespace
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: web-server
  namespace: my-app
---
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: database
  namespace: my-app # Same namespace, created once
```

## Namespace Lifecycle

### Creation

- **Automatic**: Namespaces are created automatically on the remote cluster when the first Machine in that namespace is reconciled
- **Labels**: Created namespaces include the label `managed-by: vitistack-kubevirt-operator`
- **Idempotent**: If the namespace already exists, it is reused (not recreated)

### Deletion

- **Manual**: Namespaces are **NOT** automatically deleted when Machines are removed
- **Reason**: Multiple Machines may share a namespace, and it may contain other resources
- **Cleanup**: You must manually delete namespaces from the remote cluster if desired

### Important Notes

1. **Namespace must exist on supervisor cluster first**: Create the namespace on the supervisor cluster before creating Machines in it

2. **No cross-cluster owner references**: Kubernetes doesn't support owner references across clusters, so we use labels for tracking:

   - `managed-by: vitistack-kubevirt-operator`
   - `source-machine: <machine-name>` (for VMs and PVCs)

3. **NetworkAttachmentDefinitions**: The operator looks for NADs in the same namespace on the remote cluster. If you use custom networks, ensure NADs exist in the target namespace.

## Verification

To verify namespace creation on the remote cluster:

```bash
# Get the remote cluster kubeconfig from KubevirtConfig
kubectl get kubevirtconfig -o yaml

# Use the remote kubeconfig to check namespaces
kubectl --kubeconfig=<remote-kubeconfig> get namespaces

# Verify namespace labels
kubectl --kubeconfig=<remote-kubeconfig> get namespace my-app -o yaml
```

## Troubleshooting

### Machine stuck in Creating state with namespace errors

**Symptom**: Machine shows error "failed to create namespace on remote cluster"

**Solutions**:

1. Check RBAC permissions - the operator needs `create` permission on namespaces
2. Verify the remote cluster is accessible and the KubevirtConfig is valid
3. Check if there's a conflicting namespace policy on the remote cluster

### Resources not found in expected namespace

**Symptom**: VirtualMachine or PVCs are not in the expected namespace

**Solution**: Verify the Machine's `.metadata.namespace` field matches your expectation. All resources are created in the same namespace as the Machine.

### Namespace not deleted after Machine removal

**Expected Behavior**: This is intentional. Namespaces are not automatically deleted because:

- Multiple Machines may share the namespace
- The namespace may contain other resources
- Namespace deletion requires manual confirmation

To delete a namespace:

```bash
kubectl --kubeconfig=<remote-kubeconfig> delete namespace my-app
```

## RBAC Requirements

The operator requires these permissions on the remote KubeVirt cluster:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: kubevirt-operator-remote
rules:
  - apiGroups: [""]
    resources: ["namespaces"]
    verbs: ["get", "list", "watch", "create"]
```

Note: No `delete` permission is required since namespaces are not automatically deleted.
