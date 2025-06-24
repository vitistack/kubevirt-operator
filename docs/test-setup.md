# How to create a test environment locally

## Prerequisites

- TalosCLI (https://talos.dev)
- Docker Desktop (perhaps Podman will work :thinking:) (https://docs.docker.com/desktop/setup/install/windows-install/)
- Helm (https://helm.sh/docs/intro/install/)

# Create a cluster

## Create a config

`talosctl gen config testcluster https://127.0.0.1:6443 --output ./hack/talos`

This creates three files, `controlplane.yaml` (setup for control planes), `worker.yaml` (config for workers) and `talosconfig` (self signed certificates and some other stuff)

## Create the cluster

`talosctl cluster create --name virt --controlplanes 3 --workers 3 --mtu 1500 --talosconfig ./hack/talos/talosconfig`

Adjust the number of control planes and workers.

:exclamation: **For kubevirt to work, the --mtu must be set to 1500 or another low value**

## Extract kubeconfig

Talos CLI automatically merges with your default kube config (~/.kube/config)

But if you want to extract the kubeconfig to a own file, do:

`talosctl kubeconfig ./hack/talos/cluster.kube.config --nodes 10.5.0.2 --talosconfig ./hack/talos/talosconfig`

Use it by:

`export KUBECONFIG=$(pwd)/hack/talos/cluster.kube.config`

```bash
$ kubectl get nodes
NAME                  STATUS   ROLES           AGE     VERSION
virt-controlplane-1   Ready    control-plane   6m15s   v1.33.1
virt-controlplane-2   Ready    control-plane   6m22s   v1.33.1
virt-controlplane-3   Ready    control-plane   6m18s   v1.33.1
virt-worker-1         Ready    <none>          6m11s   v1.33.1
virt-worker-2         Ready    <none>          6m12s   v1.33.1
virt-worker-3         Ready    <none>          6m15s   v1.33.1
```

# Install Kubevirt

Installs kubevirt from latest

`make k8s-install-kubevirt`

## If running in Docker Desktop / Podman

`make k8s-kubevirt-emulation-patch`

patches the kubevirt to be in a emulated environment

# Install storage

`make k8s-install-simple-storageclass`

# Cleanup / Uninstall

`talosctl cluster destroy --name <name>`
