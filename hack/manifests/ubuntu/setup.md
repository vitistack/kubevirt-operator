# setup

1. config for kind cluster, networking:disableDefaultCNI: true

```yaml
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
networking:
  disableDefaultCNI: true
  podSubnet: 192.168.0.0/16

nodes:
  - role: control-plane
    image: kindest/node:v1.33.1@sha256:8d866994839cd096b3590681c55a6fa4a071fdaf33be7b9660e5697d2ed13002
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: ingress-ready=true
    extraPortMappings:
      - containerPort: 80
        hostPort: 80
        protocol: TCP
      - containerPort: 443
        hostPort: 443
        protocol: TCP
  - role: worker
    image: kindest/node:v1.33.1@sha256:8d866994839cd096b3590681c55a6fa4a071fdaf33be7b9660e5697d2ed13002
  - role: worker
    image: kindest/node:v1.33.1@sha256:8d866994839cd096b3590681c55a6fa4a071fdaf33be7b9660e5697d2ed13002
  - role: worker
    image: kindest/node:v1.33.1@sha256:8d866994839cd096b3590681c55a6fa4a071fdaf33be7b9660e5697d2ed13002
```

2. `kind create cluster --name infra --config ./config.yaml`
3. install Calico, https://docs.tigera.io/calico/latest/getting-started/
   verify with:

```bash
watch kubectl get tigerastatus

After a few minutes, all the Calico components display True in the AVAILABLE column.

Expected output
NAME                            AVAILABLE   PROGRESSING   DEGRADED   SINCE
apiserver                       True        False         False      4m9s
calico                          True        False         False      3m29s
goldmane                        True        False         False      3m39s
ippools                         True        False         False      6m4s
whisker                         True        False         False      3m19s
```

4. install kubevirt: `make install-kubevirt`
5. patch kubevirt: `make patch-kind-for-kubevirt`
6. install kubevirt containerized data importer: `make install-kubevirt-containerized-data-importer`
7. install the multus network attatchment: `kubectl apply -f hack/manifests/ubuntu/multus-network-attatchment.yaml`
8. install the virtualmachine by: `kubectl apply -f hack/manifests/ubuntu/test.yaml`
