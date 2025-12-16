# kubevirt-operator

Kubevirt operator for creating virtual machines

## Quick Start

### Building Locally

This project has dependencies on private repositories. To build locally:

```bash
# Using SSH (recommended)
make docker-build-with-ssh

# Using Docker secrets (secure token method)
export GITHUB_TOKEN=your_token_here
make docker-build-with-secrets

# Using build args (legacy token method)
export GITHUB_TOKEN=your_token_here
make docker-build-with-token
```

# Documentation

Using https://kubebuilder.io and https://kubevirt.io/user-guide/

## Configuration

The operator can be configured using environment variables:

### Storage Configuration

- **`PVC_VOLUME_MODE`**: Set the volume mode for PersistentVolumeClaims (default: `Block`)

  - `Block`: Raw block device access for better performance (default)
  - `Filesystem`: Traditional filesystem access

  Example:

  ```bash
  export PVC_VOLUME_MODE=Filesystem
  ```

### Virtual Machine Configuration

- **`CPU_MODEL`**: CPU model for VMs (default: `host-model` for x86_64, `host-passthrough` for ARM)
- **`VM_NAME_PREFIX`**: Prefix for VM names (default: `vm-`)

### Network Configuration

- **`NETWORK_ATTACHMENT_DEFINITION_CNI_VERSION`**: CNI version for NetworkAttachmentDefinitions (default: `1.0.0`)

### Operator Configuration

- **`MANAGED_BY`**: Label value for managed resources (default: `kubevirt-operator`)
- **`DEFAULT_KUBEVIRT_CONFIG`**: Name of the default KubevirtConfig to use

> **Note:** KubevirtConfig is a cluster-scoped resource. Each KubevirtConfig specifies its own `spec.secretNamespace` where the kubeconfig secret is stored.

### Logging Configuration

- **`LOG_LEVEL`**: Logging level (default: `info`)
- **`LOG_JSON`**: Enable JSON logging (default: `true`)
- **`LOG_ADD_CALLER`**: Add caller information to logs
- **`LOG_DISABLE_STACKTRACE`**: Disable stack traces
- **`LOG_UNESCAPED_MULTILINE`**: Allow unescaped multiline logs
- **`LOG_COLORIZE_LINE`**: Colorize log lines

## CI/CD

This project uses GitHub Actions for continuous integration and delivery:

- **Build and Tests**: Runs on each push and pull request to verify code integrity.
- **Security Scan**: Regular vulnerability scanning with govulncheck and CodeQL.
- **Release Process**: Tagged commits trigger automatic builds and publish to:
  - Container images: `ghcr.io/vitistack/viti-kea-operator`
  - Helm charts: `oci://ghcr.io/vitistack/helm/kubevirt-operator`
- **Dependabot**: Automated dependency updates for GitHub Actions, Go modules, Docker, and Helm charts.

### Creating a Release

To create a new release:

1. Tag the commit: `git tag -a v1.0.0 -m "Release v1.0.0"`
2. Push the tag: `git push origin v1.0.0`

The GitHub Actions workflow will automatically build and publish the container image and Helm chart.

### Dependency Management

Dependabot is configured to automatically open pull requests for:

- GitHub Actions workflow dependencies
- Go module dependencies
- Docker image dependencies
- Helm chart dependencies

Pull requests for minor and patch updates are automatically approved and merged. Major updates require manual review and approval.

# Setup local kubernetes cluster with talos, viti crds, kubevirt and storage class

[Setup documentation](./docs/test-setup.md)
