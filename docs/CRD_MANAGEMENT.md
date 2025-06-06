# CRD Management

This controller uses Machine CRDs from the external repository `github.com/vitistack/crds`. There are two deployment approaches:

## Option 1: Standalone Deployment (Default)

The controller includes a local copy of the CRDs for self-contained deployment:

```bash
# Deploy with embedded CRDs
kubectl apply -k config/default
# OR use the helper script
./scripts/deploy.sh standalone
```

## Option 2: External CRD Management

Use this when CRDs are managed separately (e.g., by a CRD operator or separate deployment):

1. First, install CRDs from the external repository:

```bash
# Install CRDs from github.com/vitistack/crds
kubectl apply -f https://github.com/vitistack/crds/releases/download/v0.0.1-alpha04/vitistack.io_machines.yaml
```

2. Then deploy the controller without CRDs:

```bash
./scripts/deploy.sh external-crds
```

## Syncing External CRDs

To update the local CRD copy from the external repository:

```bash
./scripts/sync-external-crds.sh
```

## When to Use Each Approach

**Use Standalone (Option 1) when:**

- You want simple, self-contained deployments
- You're developing or testing locally
- You want the controller to manage its own CRDs

**Use External CRD Management (Option 2) when:**

- You have a GitOps setup where CRDs are managed separately
- Multiple controllers share the same CRDs
- You want strict separation between CRD lifecycle and controller lifecycle
- You're deploying to environments where CRD installation is restricted
