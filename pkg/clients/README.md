# Client Manager Interface

This package provides an interface-based design for managing multiple Kubernetes clients for different KubeVirt clusters.

## Interface

The `ClientManager` interface defines the contract for managing Kubernetes clients:

```go
type ClientManager interface {
    GetClientForConfig(ctx context.Context, kubevirtConfigName string) (client.Client, error)
    ListKubevirtConfigs(ctx context.Context) ([]vitistackv1alpha1.KubevirtConfig, error)
    InvalidateClient(kubevirtConfigName string)
    InvalidateAll()
    GetConfigNamespace() string
    ValidateConnection(ctx context.Context, kubevirtConfigName string) error
    GetOrCreateClientFromMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (client.Client, string, error)
    HealthCheck(ctx context.Context) map[string]error
    GetRESTConfigForConfig(ctx context.Context, kubevirtConfigName string) (*rest.Config, error)
}
```

## Implementations

### KubevirtClientManager (Production)

The production implementation that:

- Fetches KubevirtConfig CRDs from the supervisor cluster
- Creates clients from kubeconfig secrets
- Caches clients for performance
- Validates connections and performs health checks

**Usage:**

```go
import "github.com/vitistack/kubevirt-operator/pkg/clients"

clientMgr := clients.NewKubevirtClientManager(
    supervisorClient,
    scheme,
    "kubevirt-configs",
)

// Get a client for a specific config
remoteClient, err := clientMgr.GetClientForConfig(ctx, "cluster-east")

// Get client from machine - automatically assigns config if not set
client, configName, needsUpdate, err := clientMgr.GetOrCreateClientFromMachine(ctx, machine)
if err != nil {
    return err
}
// If needsUpdate is true, the config was auto-assigned and should be persisted
if needsUpdate {
    if machine.Annotations == nil {
        machine.Annotations = make(map[string]string)
    }
    machine.Annotations["vitistack.io/kubevirt-config"] = configName
    // Update the machine...
}
```

### MockClientManager (Testing)

A mock implementation for unit testing:

- Allows injecting pre-configured clients
- Tracks method calls for verification
- Supports custom behavior functions
- Can simulate errors

**Usage in tests:**

```go
import "github.com/vitistack/kubevirt-operator/pkg/clients"

// Create mock
mockMgr := clients.NewMockClientManager()

// Add a mocked client
mockMgr.AddMockedClient("test-cluster", fakeClient)

// Add a mocked config
mockMgr.AddMockedConfig(vitistackv1alpha1.KubevirtConfig{
    ObjectMeta: metav1.ObjectMeta{
        Name: "test-cluster",
        Namespace: "kubevirt-configs",
    },
    Spec: vitistackv1alpha1.KubevirtConfigSpec{
        Name: "test-cluster",
        KubeconfigSecretRef: "test-secret",
    },
})

// Use in reconciler
reconciler := NewMachineReconciler(client, scheme, mockMgr)

// Verify calls
assert.Equal(t, 1, len(mockMgr.GetClientCalls))
assert.Equal(t, "test-cluster", mockMgr.GetClientCalls[0])
```

## Benefits of Interface-Based Design

1. **Testability**: Easy to create mocks and test reconciler logic without real clusters
2. **Flexibility**: Can swap implementations (e.g., for different authentication methods)
3. **Dependency Injection**: Cleaner separation of concerns
4. **Extensibility**: Easy to add new implementations (e.g., caching strategies)
5. **Mocking**: Simplified unit testing with mock implementations

## Example Test

```go
func TestMachineReconciler_WithMockClients(t *testing.T) {
    // Setup
    mockMgr := clients.NewMockClientManager()
    fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).Build()
    mockMgr.AddMockedClient("test-cluster", fakeRemoteClient)

    reconciler := NewMachineReconciler(
        fake.NewClientBuilder().WithScheme(scheme).Build(),
        scheme,
        mockMgr,
    )

    machine := &vitistackv1alpha1.Machine{
        ObjectMeta: metav1.ObjectMeta{
            Name: "test-machine",
            Namespace: "default",
            Annotations: map[string]string{
                "vitistack.io/kubevirt-config": "test-cluster",
            },
        },
    }

    // Execute
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

## Custom Behavior Functions

The mock supports custom behavior functions for complex test scenarios:

```go
mockMgr := clients.NewMockClientManager()

// Custom function to return different clients based on config name
mockMgr.GetClientForConfigFunc = func(ctx context.Context, name string) (client.Client, error) {
    if name == "error-cluster" {
        return nil, fmt.Errorf("simulated error")
    }
    return fakeClient, nil
}

// Custom function for machine-based client selection
mockMgr.GetOrCreateClientFromMachineFunc = func(ctx context.Context, machine *vitistackv1alpha1.Machine) (client.Client, string, error) {
    // Custom logic here
    return customClient, "custom-cluster", nil
}
```

## Error Simulation

Easily simulate various error conditions:

```go
mockMgr := clients.NewMockClientManager()

// Simulate client retrieval error
mockMgr.GetClientError = fmt.Errorf("connection refused")

// Simulate list configs error
mockMgr.ListConfigsError = fmt.Errorf("permission denied")

// Simulate connection validation error
mockMgr.ValidateConnectionError = fmt.Errorf("cluster unreachable")
```
