# How to create a test environment locally

## Prerequisites

- TalosCLI (https://talos.dev)
- Docker Desktop (perhaps Podman will work :thinking:) (https://docs.docker.com/desktop/setup/install/windows-install/)
- Helm (https://helm.sh/docs/intro/install/)

# Create a cluster

## Create a config

`talosctl gen config testcluster https://127.0.0.1:6443 --output talosconfig`

This creates three files, `controlplane.yaml` (setup for control planes), `worker.yaml` (config for workers) and `talosconfig` (self signed certificates and some other stuff)

## Create the cluster

`talosctl cluster create --name virt --controlplanes 3 --workers 3 --mtu 1500 --talosconfig ./talosconfig`

Adjust the number of control planes and workers.

:exclamation: **For kubevirt to work, the --mtu must be set to 1500 or another low value**

## Extract kubeconfig

Talos CLI automatically merges with your default kube config (~/.kube/config)

But if you want to extract the kubeconfig to a own file, do:

`talosctl kubeconfig ./cluster.kube.config --nodes 10.5.0.2 --talosconfig ~/talosconfig`

Use it by:

`export KUBECONFIG=$(pwd)/kubeconfig`

```bash
$ kubectl get nodes
NAME                  STATUS   ROLES           AGE   VERSION
virt-controlplane-1   Ready    control-plane   15m   v1.33.1
virt-worker-1         Ready    <none>          15m   v1.33.1
virt-worker-2         Ready    <none>          15m   v1.33.1
virt-worker-3         Ready    <none>          15m   v1.33.1
```

# Install Kubevirt

Installs kubevirt from latest

`make k8s-install-kubevirt`

(
Patches the kubevirt to be in a emulated environment
`make k8s-kubevirt-emulation-patch`
)

# Install storage

Follow talos doc for rook ceph: https://www.talos.dev/v1.10/kubernetes-guides/configuration/ceph-with-rook/

1. Add helm repo:

`helm repo add rook-release https://charts.rook.io/release`

2. Set privileged in talos for namespace

`kubectl label namespace rook-ceph pod-security.kubernetes.io/enforce=privileged`

3. Install rook ceph cluster

`helm install --create-namespace --namespace rook-ceph rook-ceph-cluster --set operatorNamespace=rook-ceph rook-release/rook-ceph-cluster`

4. Wait for cluster to finish

`watch kubectl --namespace rook-ceph get cephcluster rook-ceph`

```bash
Every 2.0s: kubectl --namespace rook-ceph get cephcluster rook-ceph

NAME        DATADIRHOSTPATH   MONCOUNT   AGE   PHASE         MESSAGE                 HEALTH   EXTERNAL
rook-ceph   /var/lib/rook     3          57s   Progressing   Configuring Ceph Mons
```

# Cleanup / Uninstall

`talosctl cluster destroy --name <name>`
