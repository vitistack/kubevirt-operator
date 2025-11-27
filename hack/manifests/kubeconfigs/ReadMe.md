# How create a KubeVirtConfig

## Create a k8s secret from file content

`kubectl create secret generic kubevirt-provider --from-file=kubeconfig=<path to kubevirt kubeconfig file>`

## Create the KubevirtConfig

Modify the `./kubevirtconfig.yaml`

And then:

`kubectl apply -f kubevirtconfig.yaml`
