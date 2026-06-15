# Disabling the `ht` CPU Feature on host-model VMs

## Overview

The operator can add a `disable` policy for the **`ht`** CPU feature on VMs that
use the `host-model` CPU model. This is a workaround for a libvirt host-model
regression introduced in **KubeVirt ≥ v1.8.0**.

It is controlled by the `DISABLE_CPU_HT_FEATURE` environment variable
(Helm value: `disableHtCpuFeature`) and is **enabled by default**.

## The Problem

When a VM is configured with `cpuModel: "host-model"`, libvirt expands
`host-model` into an explicit list of CPU features it expects the guest to have,
to match the host CPU as closely as possible.

Starting with KubeVirt v1.8.0, that expanded feature list includes **`ht`** —
the HTT (Hyper-Threading Technology) CPUID flag, which advertises hyper-threading
topology support to the guest OS.

libvirt requests `ht`, but QEMU does not expose that flag for the guest CPU
topology. libvirt validates the result with `check='full'` (strict — every
requested feature must be present), the check fails, and the VM never starts:

```
guest CPU doesn't match specification: extra features: ht
```

The VirtualMachineInstance ends up crash-looping.

## The Fix

When `DISABLE_CPU_HT_FEATURE` is enabled **and** the CPU model is `host-model`,
the operator appends a CPU feature to the VM spec that tells libvirt not to
require `ht`:

```go
if cpuModel == "host-model" && viper.GetBool(consts.DISABLE_CPU_HT_FEATURE) {
    cpu.Features = append(cpu.Features, kubevirtv1.CPUFeature{
        Name:   "ht",
        Policy: "disable",
    })
}
```

This produces a VirtualMachine spec like:

```yaml
spec:
  template:
    spec:
      domain:
        cpu:
          model: host-model
          cores: 2
          sockets: 1
          threads: 1
          features:
            - name: ht
              policy: disable
```

With `policy: disable`, libvirt drops `ht` from the requirement list, the strict
`check='full'` validation passes, and the VM boots normally.

## What This Does NOT Do

This change is narrow and easy to misread. To be clear:

- **It does not disable hyper-threading on the host.** The physical CPU and node
  topology are untouched; HT continues to work at the hardware level.
- **It does not remove CPU cores or threads from the guest.** The guest still
  gets the `cores` / `sockets` / `threads` configured on the VM. Only the `ht`
  *capability-advertisement bit* is dropped from the guest CPU spec.
- **It does not affect nested virtualization.** Nested virt depends on the
  `vmx` (Intel) / `svm` (AMD) flags, which are independent of `ht` and are not
  touched by this workaround.

## Scope

- Applies **only** to `host-model`. For `host-passthrough` (e.g. ARM) and custom
  CPU models, the feature is never added — only `host-model` exhibits the
  regression.
- Opt-out per cluster by setting the variable to `false`.

## Configuration

### Environment variable

```bash
# Enabled by default; set to false to opt out
export DISABLE_CPU_HT_FEATURE=false
```

### Helm value

```yaml
# values.yaml
disableHtCpuFeature: true # default
```

The Helm chart maps `disableHtCpuFeature` to the `DISABLE_CPU_HT_FEATURE`
environment variable on the operator Deployment.

## When to Disable This Workaround

Leave it enabled on KubeVirt ≥ v1.8.0 clusters using `host-model`. You may set
it to `false` if:

- You are on a KubeVirt version that does not have the regression, or
- A future KubeVirt/libvirt release fixes the underlying issue and the explicit
  `disable` policy is no longer needed.

## References

- CPU model: `internal/machine/vm/vm_manager.go` (`buildVMSpec`)
- Constant: `internal/consts/app_consts.go` (`DISABLE_CPU_HT_FEATURE`)
- Default: `internal/settings/settings.go`
- KubeVirt CPU configuration: https://kubevirt.io/user-guide/
