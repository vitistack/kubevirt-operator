/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package vm

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

const (
	// CloudInitDiskName is the name of the cloudInitNoCloud disk/volume.
	CloudInitDiskName = "cloudinitdisk"

	defaultUserDataSecretKey    = "userdata"
	defaultNetworkDataSecretKey = "networkdata"

	// noopUserData is a minimal valid cloud-init payload used when no
	// user-data source is configured (Option A: Talos stays in maintenance
	// mode and is configured later via talosctl apply-config). Without this,
	// KubeVirt omits the user-data field entirely (omitempty), cidata ends
	// up with an empty/missing user-data file, and Talos logs a noisy
	// "config not found" error on every reconcile.
	noopUserData = "#cloud-config\n"
)

// ErrWaitingForStaticIP signals the Machine references a static NetworkNamespace
// but static-ip-operator has not yet populated NetworkConfiguration.status with
// an IP for the VM's MAC. The reconciler should requeue without marking Failed.
var ErrWaitingForStaticIP = errors.New("waiting for static IP allocation from static-ip-operator")

// ErrWaitingForCloudInitSource signals the referenced user/network data Secret
// or ConfigMap does not exist yet. Reconciler should requeue.
var ErrWaitingForCloudInitSource = errors.New("waiting for cloud-init source to exist")

// cloudInitBundle is the resolved cidata content for a Machine.
type cloudInitBundle struct {
	UserData    string
	NetworkData string // empty when not needed (DHCP / no NetworkNamespace)
}

// resolveCloudInit returns the cidata payload for a Machine, or nil if the
// Machine has no cloudInit config. Returns ErrWaitingForStaticIP when a static
// NetworkNamespace is referenced but its NetworkConfiguration.status has no IP
// allocated for the relevant interface yet.
func (m *VMManager) resolveCloudInit(ctx context.Context, machine *vitistackv1alpha1.Machine) (*cloudInitBundle, error) {
	if machine.Spec.CloudInit == nil {
		return nil, nil
	}

	userData, err := m.resolveUserData(ctx, machine)
	if err != nil {
		return nil, err
	}

	networkData, err := m.resolveNetworkData(ctx, machine)
	if err != nil {
		return nil, err
	}

	return &cloudInitBundle{
		UserData:    userData,
		NetworkData: networkData,
	}, nil
}

func (m *VMManager) resolveUserData(ctx context.Context, machine *vitistackv1alpha1.Machine) (string, error) {
	ci := machine.Spec.CloudInit
	switch {
	case ci.UserData != "":
		return ci.UserData, nil
	case ci.UserDataBase64 != "":
		decoded, err := base64.StdEncoding.DecodeString(ci.UserDataBase64)
		if err != nil {
			return "", fmt.Errorf("decode userDataBase64: %w", err)
		}
		return string(decoded), nil
	case ci.UserDataSecretRef != nil:
		return m.readSecretKey(ctx, machine.Namespace, ci.UserDataSecretRef.Name, ci.UserDataSecretRef.Key, defaultUserDataSecretKey)
	case ci.UserDataConfigMapRef != nil:
		return m.readConfigMapKey(ctx, machine.Namespace, ci.UserDataConfigMapRef.Name, ci.UserDataConfigMapRef.Key, defaultUserDataSecretKey)
	}
	return noopUserData, nil
}

func (m *VMManager) resolveNetworkData(ctx context.Context, machine *vitistackv1alpha1.Machine) (string, error) {
	ci := machine.Spec.CloudInit

	switch {
	case ci.NetworkData != "":
		return ci.NetworkData, nil
	case ci.NetworkDataBase64 != "":
		decoded, err := base64.StdEncoding.DecodeString(ci.NetworkDataBase64)
		if err != nil {
			return "", fmt.Errorf("decode networkDataBase64: %w", err)
		}
		return string(decoded), nil
	case ci.NetworkDataSecretRef != nil:
		return m.readSecretKey(ctx, machine.Namespace, ci.NetworkDataSecretRef.Name, ci.NetworkDataSecretRef.Key, defaultNetworkDataSecretKey)
	case ci.NetworkDataConfigMapRef != nil:
		return m.readConfigMapKey(ctx, machine.Namespace, ci.NetworkDataConfigMapRef.Name, ci.NetworkDataConfigMapRef.Key, defaultNetworkDataSecretKey)
	}

	return m.synthesizeNetworkDataFromStaticIP(ctx, machine)
}

// synthesizeNetworkDataFromStaticIP builds a cloud-init network-config v1 YAML
// by pairing the MAC on NetworkConfiguration.spec.networkInterfaces[] with the
// IP that static-ip-operator wrote into .status.networkInterfaces[], but only
// when the referenced NetworkNamespace uses static allocation.
func (m *VMManager) synthesizeNetworkDataFromStaticIP(ctx context.Context, machine *vitistackv1alpha1.Machine) (string, error) {
	netNsName := machine.Spec.Network.NetworkNamespaceName
	if netNsName == "" {
		return "", nil
	}

	netNs := &vitistackv1alpha1.NetworkNamespace{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{Name: netNsName, Namespace: machine.Namespace}, netNs); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("get NetworkNamespace %q: %w", netNsName, err)
	}

	if netNs.Spec.IPAllocation == nil || netNs.Spec.IPAllocation.Type != vitistackv1alpha1.IPAllocationTypeStatic {
		return "", nil
	}

	nc := &vitistackv1alpha1.NetworkConfiguration{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{Name: machine.Name, Namespace: machine.Namespace}, nc); err != nil {
		if apierrors.IsNotFound(err) {
			return "", ErrWaitingForStaticIP
		}
		return "", fmt.Errorf("get NetworkConfiguration %q: %w", machine.Name, err)
	}

	return renderNetworkConfigV1(netNs, nc)
}

// renderNetworkConfigV1 emits cloud-init network-config v1 YAML.
// Spec: https://cloudinit.readthedocs.io/en/latest/reference/network-config-format-v1.html
func renderNetworkConfigV1(netNs *vitistackv1alpha1.NetworkNamespace, nc *vitistackv1alpha1.NetworkConfiguration) (string, error) {
	static := netNs.Spec.IPAllocation.Static
	if static == nil {
		return "", fmt.Errorf("NetworkNamespace %q has type=static but no static config", netNs.Name)
	}

	netmask, err := cidrToIPv4Netmask(static.IPv4CIDR)
	if err != nil {
		return "", err
	}

	statusIPsByMac := map[string][]string{}
	for i := range nc.Status.NetworkInterfaces {
		iface := &nc.Status.NetworkInterfaces[i]
		if iface.MacAddress == "" {
			continue
		}
		statusIPsByMac[strings.ToLower(iface.MacAddress)] = iface.IPv4Addresses
	}

	if len(nc.Spec.NetworkInterfaces) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("version: 1\nconfig:\n")
	for i := range nc.Spec.NetworkInterfaces {
		spec := &nc.Spec.NetworkInterfaces[i]
		ips := statusIPsByMac[strings.ToLower(spec.MacAddress)]
		if len(ips) == 0 {
			return "", ErrWaitingForStaticIP
		}
		name := spec.Name
		if name == "" {
			name = fmt.Sprintf("eth%d", i)
		}
		fmt.Fprintf(&b, "  - type: physical\n    name: %s\n", name)
		if spec.MacAddress != "" {
			fmt.Fprintf(&b, "    mac_address: %q\n", strings.ToLower(spec.MacAddress))
		}
		b.WriteString("    subnets:\n")
		fmt.Fprintf(&b, "      - type: static\n        address: %s\n        netmask: %s\n", ips[0], netmask)
		if static.IPv4Gateway != "" {
			fmt.Fprintf(&b, "        gateway: %s\n", static.IPv4Gateway)
		}
		if len(static.DNS) > 0 {
			b.WriteString("        dns_nameservers:\n")
			for _, d := range static.DNS {
				fmt.Fprintf(&b, "          - %s\n", d)
			}
		}
	}
	return b.String(), nil
}

func cidrToIPv4Netmask(cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", fmt.Errorf("parse CIDR %q: %w", cidr, err)
	}
	if len(ipnet.Mask) != net.IPv4len {
		return "", fmt.Errorf("CIDR %q is not IPv4", cidr)
	}
	m := ipnet.Mask
	return fmt.Sprintf("%d.%d.%d.%d", m[0], m[1], m[2], m[3]), nil
}

func (m *VMManager) readSecretKey(ctx context.Context, ns, name, key, defaultKey string) (string, error) {
	if key == "" {
		key = defaultKey
	}
	secret := &corev1.Secret{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("%w: Secret %q/%q", ErrWaitingForCloudInitSource, ns, name)
		}
		return "", fmt.Errorf("get Secret %q: %w", name, err)
	}
	v, ok := secret.Data[key]
	if !ok {
		// talos-operator writes role templates into the cluster secret after
		// generating the config bundle (which happens after the first CP VM
		// is reachable). Until then, the secret exists but lacks the key —
		// treat as transient so the reconciler requeues instead of Failing.
		return "", fmt.Errorf("%w: Secret %q has no key %q", ErrWaitingForCloudInitSource, name, key)
	}
	return string(v), nil
}

func (m *VMManager) readConfigMapKey(ctx context.Context, ns, name, key, defaultKey string) (string, error) {
	if key == "" {
		key = defaultKey
	}
	cm := &corev1.ConfigMap{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("%w: ConfigMap %q/%q", ErrWaitingForCloudInitSource, ns, name)
		}
		return "", fmt.Errorf("get ConfigMap %q: %w", name, err)
	}
	v, ok := cm.Data[key]
	if !ok {
		return "", fmt.Errorf("%w: ConfigMap %q has no key %q", ErrWaitingForCloudInitSource, name, key)
	}
	return v, nil
}

// addCloudInitDisk appends a cloudInitNoCloud disk+volume pair to the VM.
// KubeVirt composes the cidata image (cidata label + meta-data/user-data/
// network-data files) from the inline strings — no DataVolume or ISO builder
// needed. Meta-data (instance-id, local-hostname) is generated by KubeVirt
// from the VMI identity.
func addCloudInitDisk(disks []kubevirtv1.Disk, volumes []kubevirtv1.Volume, bundle *cloudInitBundle) ([]kubevirtv1.Disk, []kubevirtv1.Volume) {
	disks = append(disks, kubevirtv1.Disk{
		Name: CloudInitDiskName,
		DiskDevice: kubevirtv1.DiskDevice{
			Disk: &kubevirtv1.DiskTarget{Bus: BusTypeVirtio},
		},
	})
	volumes = append(volumes, kubevirtv1.Volume{
		Name: CloudInitDiskName,
		VolumeSource: kubevirtv1.VolumeSource{
			CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{
				UserData:    bundle.UserData,
				NetworkData: bundle.NetworkData,
			},
		},
	})
	return disks, volumes
}
