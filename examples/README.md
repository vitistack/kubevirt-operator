# Machine Examples

This directory contains example Machine manifests demonstrating various configurations.

## Boot Methods

### 1. PXE Boot (Default)

Standard PXE network boot is the default method. No special annotations required.

**Examples:**

- `machine-small.yaml`
- `machine-medium.yaml`
- `machine-large.yaml`

### 2. ISO Boot via DataVolume

Boot from an ISO image using KubeVirt's CDI (Containerized Data Importer).

**Example:** `machine-boot-iso.yaml`

**Key annotations:**

```yaml
annotations:
  kubevirt.io/boot-source: "datavolume"
  kubevirt.io/boot-source-type: "http" # or "registry"
```

**Configuration:**

```yaml
spec:
  os:
    imageID: "https://releases.ubuntu.com/24.04/ubuntu-24.04.1-live-server-amd64.iso"
```

The ISO will be:

- Downloaded via CDI into a DataVolume
- Attached as a CDROM device with boot order 1
- Root disk gets boot order 2

**Supported sources:**

- `http/https`: Direct URL to ISO file
- `registry`: Container registry with disk image

### 3. Direct Kernel Boot (vmlinuz)

Boot directly from kernel and initrd files using container images.

**Example:** `machine-boot-vmlinuz.yaml`

**Key annotations:**

```yaml
annotations:
  kubevirt.io/boot-source: "kernel"
  kubevirt.io/kernel-args: "talos.platform=metal console=ttyS0" # Optional
```

**Configuration:**

```yaml
spec:
  os:
    distribution: talos # Used for default kernel args if not specified
    imageID: "ghcr.io/siderolabs/talos:v1.11.5" # Container with kernel/initrd
```

**How it works:**

- The container image must include kernel at `/boot/vmlinuz` and initrd at `/boot/initramfs`
- Kernel arguments can be customized via annotation or use OS-specific defaults
- Default kernel args by distribution:
  - `talos`: `talos.platform=metal console=ttyS0`
  - `flatcar`: `flatcar.autologin console=ttyS0`
  - Others: `console=ttyS0 console=tty0`

**Supported distributions:**

- Talos Linux
- Flatcar Container Linux
- Any OS with a compatible container image containing kernel/initrd

## Resource Configuration Examples

### Instance Types

- `machine-small.yaml` - 1 CPU, 1GB RAM
- `machine-medium.yaml` - 2 CPU, 2GB RAM
- `machine-large.yaml` - 4 CPU, 4GB RAM

### With Overrides

- `machine-small-with-overrides.yaml` - Custom CPU/memory beyond instance type
- `machine-medium-with-overrides.yaml` - Custom resource allocation

### Disk Configuration

- `machine-with-disks.yaml` - Multiple disks with custom sizes
- `machine-disk-examples.yaml` - Advanced disk configurations

### Networking

- `machine-custom-namespace.yaml` - Custom namespace configuration

## Common Fields

### Required

```yaml
spec:
  provider: kubevirt # Must be "kubevirt"
  instanceType: "medium" # small, medium, or large
```

### Optional Resource Overrides

```yaml
spec:
  cpu:
    cores: 4
    sockets: 1
    threadsPerCore: 2
  memory: 4294967296 # bytes (4GB)
```

### Optional Disk Configuration

```yaml
spec:
  disks:
    - name: "root"
      sizeGB: 50
      boot: true
      type: "virtio"
      encrypted: false
```

## Usage

Apply any example:

```bash
kubectl apply -f examples/machine-small.yaml
```

Check status:

```bash
kubectl get machines
kubectl describe machine example-machine-small
```

## Notes

- **PXE Boot**: Works out of the box, requires network configuration
- **ISO Boot**: Requires CDI installed in the KubeVirt cluster
- **Kernel Boot**: Under development, not recommended for production yet
- **Namespace**: Machines are created in the specified namespace, VMs are created in the same namespace on the remote KubeVirt cluster
