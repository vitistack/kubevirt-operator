# KubeVirt Operator - Updated for CRD v0.0.1-alpha05

This operator has been updated to work with the external CRD package `github.com/vitistack/crds v0.0.1-alpha05`.

## Key Changes

### Field Mappings

The controller has been updated to use the correct field names from the external CRD:

| External CRD Field | Local Usage                | Description                                      |
| ------------------ | -------------------------- | ------------------------------------------------ |
| `spec.machineType` | `machine.Spec.MachineType` | Machine type (small, medium, large)              |
| `spec.memory`      | `machine.Spec.Memory`      | Memory in bytes (optional override)              |
| `spec.cpu.cores`   | `machine.Spec.CPU.Cores`   | Number of CPU cores (optional override)          |
| `status.phase`     | `machine.Status.Phase`     | Current phase (Pending, Creating, Ready, Failed) |

### Machine Types

The controller supports predefined machine types with default resource allocations:

- **small**: 1 GB RAM, 1 CPU core
- **medium**: 2 GB RAM, 2 CPU cores
- **large**: 4 GB RAM, 4 CPU cores

### Resource Overrides

You can override the default resources by specifying:

- `spec.memory`: Memory in bytes
- `spec.cpu.cores`: Number of CPU cores

## Example Usage

See `examples/machine-sample.yaml` for example Machine resources:

```yaml
apiVersion: vitistack.io/v1alpha1
kind: Machine
metadata:
  name: example-machine
  namespace: default
spec:
  machineType: "medium"
  memory: 3221225472 # 3 GB in bytes
  cpu:
    cores: 3
```

## Controller Behavior

1. **Machine Creation**: When a Machine resource is created, the controller:

   - Creates a corresponding KubeVirt VirtualMachine
   - Sets resource requests based on machine type or overrides
   - Uses a simple container disk image for demonstration

2. **Status Updates**: The controller updates the Machine status based on the VirtualMachine state:

   - `Phase: "Pending"` - Initial state
   - `Phase: "Creating"` - VirtualMachine is being created
   - `Phase: "Ready"` - VirtualMachine is running
   - `Phase: "Failed"` - Error occurred

3. **Cleanup**: When a Machine is deleted, the controller removes the associated VirtualMachine.

## Building and Running

```bash
# Build the controller
go build -v ./cmd/main.go

# Generate and install CRDs
make install

# Run the controller locally
go run ./cmd/main.go
```

## Dependencies

- External CRD: `github.com/vitistack/crds v0.0.1-alpha05`
- KubeVirt API: `kubevirt.io/api/core/v1`
- Controller Runtime: `sigs.k8s.io/controller-runtime`
