# Interface-Based Design for Client Management

## Overview

The KubeVirt operator uses an interface-based design for managing Kubernetes clients to different KubeVirt clusters. This approach provides better testability, flexibility, and maintainability.

## Architecture

```
┌─────────────────────────────────────────────────┐
│         ClientManager Interface                 │
│  (pkg/clients/interface.go)                    │
│                                                 │
│  - GetClientForConfig()                        │
│  - GetOrCreateClientFromMachine()              │
│  - ListKubevirtConfigs()                       │
│  - ValidateConnection()                        │
│  - HealthCheck()                               │
│  - ...                                         │
└─────────────────────────────────────────────────┘
                    ▲
                    │ implements
        ┌───────────┴───────────┐
        │                       │
┌───────┴───────────┐  ┌────────┴──────────┐
│ KubevirtClient    │  │ MockClient        │
│ Manager           │  │ Manager           │
│ (Production)      │  │ (Testing)         │
└───────────────────┘  └───────────────────┘
        │                       │
        │                       │
        ▼                       ▼
┌───────────────────────────────────────────────┐
│      MachineReconciler                        │
│  (depends on ClientManager interface)         │
└───────────────────────────────────────────────┘
```

## ClientManager Interface

The `ClientManager` interface defines all operations for managing multiple Kubernetes clients:

```go
type ClientManager interface {
    // Core client operations
    GetClientForConfig(ctx context.Context, kubevirtConfigName string) (client.Client, error)
    GetOrCreateClientFromMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (client.Client, string, error)

    // Config management
    ListKubevirtConfigs(ctx context.Context) ([]vitistackv1alpha1.KubevirtConfig, error)
    GetConfigNamespace() string

    // Cache management
    InvalidateClient(kubevirtConfigName string)
    InvalidateAll()

    // Health & validation
    ValidateConnection(ctx context.Context, kubevirtConfigName string) error
    HealthCheck(ctx context.Context) map[string]error

    // Advanced operations
    GetRESTConfigForConfig(ctx context.Context, kubevirtConfigName string) (*rest.Config, error)
}
```

## Implementations

### 1. KubevirtClientManager (Production)

**Location:** `pkg/clients/kubevirt_client_manager.go`

**Purpose:** Production implementation used in the operator

**Key Features:**

- Fetches KubevirtConfig CRDs from supervisor cluster
- Creates clients from kubeconfig secrets
- Caches clients for performance
- Thread-safe with mutex protection
- Validates connections and performs health checks

**Usage:**

```go
// Create the manager
clientMgr := clients.NewKubevirtClientManager(
    supervisorClient,
    scheme,
    "kubevirt-configs",
)

// Get a client for a specific config
remoteClient, err := clientMgr.GetClientForConfig(ctx, "cluster-east")

// Get client from machine
client, configName, err := clientMgr.GetOrCreateClientFromMachine(ctx, machine)
```

### 2. MockClientManager (Testing)

**Location:** `pkg/clients/mock_client_manager.go`

**Purpose:** Mock implementation for unit tests

**Key Features:**

- Pre-configure clients for testing
- Track method calls
- Simulate errors
- Custom behavior functions
- No external dependencies

**Usage:**

```go
// Create mock
mockMgr := clients.NewMockClientManager()

// Add a mocked client
fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
mockMgr.AddMockedClient("test-cluster", fakeClient)

// Add a mocked config
mockMgr.AddMockedConfig(testConfig)

// Use in reconciler
reconciler := NewMachineReconciler(client, scheme, mockMgr)

// Verify calls
assert.Equal(t, 1, len(mockMgr.GetClientCalls))
```

## Benefits

### 1. Testability

**Before (concrete type):**

```go
type MachineReconciler struct {
    KubevirtClientMgr *clients.KubevirtClientManager  // Hard to test
}

// Testing requires real KubevirtConfig CRDs and Secrets
```

**After (interface):**

```go
type MachineReconciler struct {
    KubevirtClientMgr clients.ClientManager  // Easy to mock
}

// Testing uses MockClientManager - no CRDs needed
func TestReconcile(t *testing.T) {
    mockMgr := clients.NewMockClientManager()
    // ... easy testing
}
```

### 2. Flexibility

Different implementations for different scenarios:

- **Production:** Full KubevirtConfig-based implementation
- **Testing:** Mock with pre-configured responses
- **Future:** Could add caching strategies, connection pooling, etc.

### 3. Dependency Injection

Clean separation of concerns:

```go
// Reconciler doesn't know which implementation it's using
func NewMachineReconciler(
    c client.Client,
    scheme *runtime.Scheme,
    kubevirtClientMgr clients.ClientManager,  // Interface, not concrete type
) *MachineReconciler {
    // ...
}
```

### 4. Extensibility

Easy to add new implementations:

```go
// Example: Caching client manager
type CachingClientManager struct {
    delegate ClientManager
    cache    *ttlcache.Cache
}

func (c *CachingClientManager) GetClientForConfig(ctx context.Context, name string) (client.Client, error) {
    if cached := c.cache.Get(name); cached != nil {
        return cached.(client.Client), nil
    }
    return c.delegate.GetClientForConfig(ctx, name)
}
```

## Testing Examples

### Basic Mock Usage

```go
func TestMachineReconcile_Success(t *testing.T) {
    // Setup
    mockMgr := clients.NewMockClientManager()
    fakeRemoteClient := fake.NewClientBuilder().WithScheme(scheme).Build()
    mockMgr.AddMockedClient("cluster-east", fakeRemoteClient)

    reconciler := v1alpha1.NewMachineReconciler(
        fake.NewClientBuilder().WithScheme(scheme).Build(),
        scheme,
        mockMgr,
    )

    // Test
    result, err := reconciler.Reconcile(ctx, req)

    // Verify
    assert.NoError(t, err)
    assert.False(t, result.Requeue)
}
```

### Error Simulation

```go
func TestMachineReconcile_ClientError(t *testing.T) {
    mockMgr := clients.NewMockClientManager()
    mockMgr.GetClientError = fmt.Errorf("connection refused")

    reconciler := v1alpha1.NewMachineReconciler(
        fake.NewClientBuilder().WithScheme(scheme).Build(),
        scheme,
        mockMgr,
    )

    result, err := reconciler.Reconcile(ctx, req)

    // Verify error handling
    assert.Error(t, err)
    assert.Contains(t, err.Error(), "connection refused")
}
```

### Custom Behavior

```go
func TestMachineReconcile_DynamicClient(t *testing.T) {
    mockMgr := clients.NewMockClientManager()

    // Different client for each config
    mockMgr.GetClientForConfigFunc = func(ctx context.Context, name string) (client.Client, error) {
        switch name {
        case "prod":
            return prodClient, nil
        case "staging":
            return stagingClient, nil
        default:
            return nil, fmt.Errorf("unknown cluster: %s", name)
        }
    }

    // Test with different clusters
}
```

### Call Tracking

```go
func TestClientManager_CallTracking(t *testing.T) {
    mockMgr := clients.NewMockClientManager()
    mockMgr.AddMockedClient("test", fakeClient)

    // Make some calls
    _, _ = mockMgr.GetClientForConfig(ctx, "test")
    _, _ = mockMgr.GetClientForConfig(ctx, "test")
    mockMgr.InvalidateClient("test")

    // Verify
    assert.Equal(t, 2, len(mockMgr.GetClientCalls))
    assert.Equal(t, "test", mockMgr.GetClientCalls[0])
    assert.Equal(t, 1, len(mockMgr.InvalidateClientCalls))
}
```

## Migration from Concrete Type

If you have existing code using the concrete type:

**Before:**

```go
type MachineReconciler struct {
    KubevirtClientMgr *clients.KubevirtClientManager
}

func NewMachineReconciler(c client.Client, scheme *runtime.Scheme) *MachineReconciler {
    clientMgr := clients.NewKubevirtClientManager(c, scheme, "default")
    return &MachineReconciler{
        KubevirtClientMgr: clientMgr,
    }
}
```

**After:**

```go
type MachineReconciler struct {
    KubevirtClientMgr clients.ClientManager  // Interface
}

func NewMachineReconciler(
    c client.Client,
    scheme *runtime.Scheme,
    clientMgr clients.ClientManager,  // Injected
) *MachineReconciler {
    return &MachineReconciler{
        KubevirtClientMgr: clientMgr,
    }
}

// In main.go
clientMgr := clients.NewKubevirtClientManager(c, scheme, namespace)
reconciler := v1alpha1.NewMachineReconciler(c, scheme, clientMgr)
```

## Best Practices

### 1. Always Use the Interface

```go
// Good
func ProcessMachine(clientMgr clients.ClientManager, machine *Machine) error {
    // ...
}

// Bad
func ProcessMachine(clientMgr *clients.KubevirtClientManager, machine *Machine) error {
    // ...
}
```

### 2. Inject Dependencies

```go
// Good - dependency injection
func NewReconciler(clientMgr clients.ClientManager) *Reconciler {
    return &Reconciler{clientMgr: clientMgr}
}

// Bad - creating dependency internally
func NewReconciler(client client.Client, scheme *runtime.Scheme) *Reconciler {
    clientMgr := clients.NewKubevirtClientManager(client, scheme, "default")
    return &Reconciler{clientMgr: clientMgr}
}
```

### 3. Use Mocks in Tests

```go
// Good
func TestReconcile(t *testing.T) {
    mockMgr := clients.NewMockClientManager()
    // ...
}

// Bad - testing with real implementation
func TestReconcile(t *testing.T) {
    realMgr := clients.NewKubevirtClientManager(...)
    // Requires real KubevirtConfig CRDs
}
```

### 4. Keep Interface Focused

The `ClientManager` interface should only contain methods related to client management. Don't add unrelated functionality.

## Future Enhancements

### Possible New Implementations

1. **CachingClientManager**: Add TTL-based caching layer
2. **PooledClientManager**: Connection pooling for better resource management
3. **MetricsClientManager**: Wrapper that tracks metrics (calls, errors, latency)
4. **ResilientClientManager**: Automatic retry and circuit breaker patterns

### Example: Metrics Wrapper

```go
type MetricsClientManager struct {
    delegate ClientManager
    metrics  *prometheus.MetricVec
}

func (m *MetricsClientManager) GetClientForConfig(ctx context.Context, name string) (client.Client, error) {
    start := time.Now()
    client, err := m.delegate.GetClientForConfig(ctx, name)
    duration := time.Since(start)

    m.metrics.WithLabelValues(name, errorStatus(err)).Observe(duration.Seconds())
    return client, err
}
```

## Conclusion

The interface-based design provides:

- ✅ Better testability with mock implementations
- ✅ Cleaner dependency injection
- ✅ Flexibility for different deployment scenarios
- ✅ Extensibility for future enhancements
- ✅ Better separation of concerns

This makes the codebase more maintainable and easier to test while maintaining all the functionality of the concrete implementation.
