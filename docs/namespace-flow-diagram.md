# Namespace Management Flow Diagram

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────┐
│                      SUPERVISOR CLUSTER                          │
│                                                                  │
│  ┌────────────────────┐         ┌──────────────────────┐       │
│  │   User creates     │         │  KubeVirt Operator   │       │
│  │   Namespace        │────────▶│  (Reconciler)        │       │
│  │   + Machine CRD    │         │                      │       │
│  └────────────────────┘         └──────────────────────┘       │
│                                            │                     │
└────────────────────────────────────────────┼─────────────────────┘
                                             │
                                             │ Remote Client
                                             │ (from KubevirtConfig)
                                             ▼
┌─────────────────────────────────────────────────────────────────┐
│                    REMOTE KUBEVIRT CLUSTER                       │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ 1. ensureNamespaceExists()                               │  │
│  │    ✓ Check if namespace exists                           │  │
│  │    ✓ Create if missing (with managed-by label)          │  │
│  └──────────────────────────────────────────────────────────┘  │
│                          │                                      │
│                          ▼                                      │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │ 2. Create Resources                                      │  │
│  │    • VirtualMachine (in namespace)                       │  │
│  │    • PersistentVolumeClaims (in namespace)              │  │
│  │    • Network resources (in namespace)                    │  │
│  └──────────────────────────────────────────────────────────┘  │
│                                                                  │
└──────────────────────────────────────────────────────────────────┘
```

## Reconciliation Flow

```
┌───────────────────────────────────────────────────────────────────┐
│  Machine Reconcile Loop                                           │
└───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌───────────────────────────────────────────────────────────────────┐
│  1. Get Machine from Supervisor Cluster                           │
└───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌───────────────────────────────────────────────────────────────────┐
│  2. Get Remote KubeVirt Client (from KubevirtConfig)             │
└───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌───────────────────────────────────────────────────────────────────┐
│  3. **NEW** Ensure Namespace Exists on Remote Cluster            │
│     ┌─────────────────────────────────────────────────────────┐  │
│     │ ensureNamespaceExists(ctx, machine.Namespace, remote)   │  │
│     │                                                         │  │
│     │  namespace := &corev1.Namespace{}                      │  │
│     │  err := remoteClient.Get(ctx, name, namespace)         │  │
│     │                                                         │  │
│     │  if NotFound:                                          │  │
│     │    namespace = &corev1.Namespace{                      │  │
│     │      Name: machine.Namespace,                          │  │
│     │      Labels: {"managed-by": "vitistack-..."}          │  │
│     │    }                                                   │  │
│     │    remoteClient.Create(ctx, namespace)                 │  │
│     └─────────────────────────────────────────────────────────┘  │
└───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌───────────────────────────────────────────────────────────────────┐
│  4. Get or Create VirtualMachine                                  │
│     (now guaranteed namespace exists)                             │
└───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌───────────────────────────────────────────────────────────────────┐
│  5. Create PVCs (if needed)                                       │
│     (now guaranteed namespace exists)                             │
└───────────────────────────────────────────────────────────────────┘
          │
          ▼
┌───────────────────────────────────────────────────────────────────┐
│  6. Update Machine Status                                         │
└───────────────────────────────────────────────────────────────────┘
```

## Namespace Lifecycle

### Creation Flow

```
User Action              Operator Action              Result
───────────              ───────────────              ──────

kubectl create ns        (on supervisor)              ✓ Namespace on supervisor
  my-namespace
                              │
                              ▼
kubectl apply -f         Reconcile triggered
  machine.yaml
  (namespace:                 │
   my-namespace)              ▼

                         Get remote client
                              │
                              ▼

                         Check if namespace
                         exists on remote
                              │
                              ▼

                         Namespace not found
                              │
                              ▼

                         Create namespace             ✓ Namespace on remote
                         with labels:                    with managed-by label
                         - managed-by
                              │
                              ▼

                         Create VM, PVCs              ✓ VM running in namespace
                         in namespace                 ✓ PVCs in namespace
```

### Deletion Flow

```
User Action              Operator Action              Result
───────────              ───────────────              ──────

kubectl delete           Reconcile deletion
  machine my-vm
  -n my-namespace             │
                              ▼

                         Delete VirtualMachine        ✓ VM deleted
                         from remote cluster
                              │
                              ▼

                         Delete PVCs                  ✓ PVCs deleted
                         from remote cluster
                              │
                              ▼

                         Remove finalizer             ✓ Machine deleted
                              │
                              ▼

                         Namespace REMAINS            ℹ Namespace preserved
                         (not deleted)                  (manual cleanup needed
                                                        if desired)
```

## Error Scenarios

### Scenario 1: Namespace Creation Fails

```
┌─────────────────────────────────────────┐
│ ensureNamespaceExists()                 │
│   ├─ Get namespace: NotFound           │
│   ├─ Create namespace: FAILED           │
│   │  (e.g., RBAC denied)               │
│   └─ Return error                       │
└─────────────────────────────────────────┘
                │
                ▼
┌─────────────────────────────────────────┐
│ Reconcile returns with error            │
│ RequeueAfter: 1 minute                  │
└─────────────────────────────────────────┘
                │
                ▼
┌─────────────────────────────────────────┐
│ Machine stuck in Creating state         │
│ Error logged for debugging              │
└─────────────────────────────────────────┘
```

### Scenario 2: Namespace Already Exists

```
┌─────────────────────────────────────────┐
│ ensureNamespaceExists()                 │
│   ├─ Get namespace: SUCCESS             │
│   │  (namespace exists)                 │
│   └─ Return without creating            │
└─────────────────────────────────────────┘
                │
                ▼
┌─────────────────────────────────────────┐
│ Continue with VM creation               │
│ No namespace modification               │
│ Existing labels preserved               │
└─────────────────────────────────────────┘
```

## Multi-Tenant Scenario

```
SUPERVISOR CLUSTER              REMOTE KUBEVIRT CLUSTER
──────────────────              ───────────────────────

Namespace: team-a               Namespace: team-a ✓ (auto-created)
  Machine: app1                   VirtualMachine: vm-app1
  Machine: app2                   VirtualMachine: vm-app2
                                  PVC: vm-app1-root-pvc
                                  PVC: vm-app2-root-pvc

Namespace: team-b               Namespace: team-b ✓ (auto-created)
  Machine: db1                    VirtualMachine: vm-db1
                                  PVC: vm-db1-root-pvc

Namespace: default              Namespace: default (pre-existing)
  Machine: test                   VirtualMachine: vm-test
                                  PVC: vm-test-root-pvc
```

## Labels and Tracking

```
Created Namespace:
┌────────────────────────────────────────────────────────┐
│ apiVersion: v1                                         │
│ kind: Namespace                                        │
│ metadata:                                              │
│   name: my-namespace                                   │
│   labels:                                              │
│     managed-by: vitistack-kubevirt-operator  ◄──────── │
│                                              tracking  │
└────────────────────────────────────────────────────────┘

Resources in Namespace:
┌────────────────────────────────────────────────────────┐
│ VirtualMachine:                                        │
│   labels:                                              │
│     managed-by: vitistack-kubevirt-operator            │
│     source-machine: my-vm  ◄─────────────────────────  │
│                                            links back  │
│ PVC:                                         to Machine│
│   labels:                                              │
│     managed-by: vitistack-kubevirt-operator            │
│     source-machine: my-vm  ◄─────────────────────────  │
└────────────────────────────────────────────────────────┘
```
