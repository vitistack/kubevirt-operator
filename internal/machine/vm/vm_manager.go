/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vm

import (
	"context"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	"github.com/vitistack/kubevirt-operator/internal/machine/network"
	"github.com/vitistack/kubevirt-operator/internal/machine/status"
	"github.com/vitistack/kubevirt-operator/pkg/macaddress"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// VMManager handles VirtualMachine-related operations
type VMManager struct {
	supervisorClient client.Client // Client for supervisor cluster (Machine CRDs)
	remoteClient     client.Client // Client for remote KubeVirt cluster (VMs)
	Scheme           *runtime.Scheme
	NetworkManager   *network.NetworkManager
	MacGenerator     macaddress.MacAddressGenerator
	StatusManager    *status.StatusManager
}

// NewManager creates a new VM manager
// The client parameter is the supervisor cluster client
func NewManager(c client.Client, scheme *runtime.Scheme, macGenerator macaddress.MacAddressGenerator, statusManager *status.StatusManager) *VMManager {
	return &VMManager{
		supervisorClient: c,
		remoteClient:     c, // Default to supervisor client for backward compatibility
		Scheme:           scheme,
		MacGenerator:     macGenerator,
		StatusManager:    statusManager,
		NetworkManager:   network.NewManager(c), // Initialize NetworkManager with supervisor client
	}
}

// SetRemoteClient sets the remote KubeVirt cluster client
// This should be called before performing VM operations
func (m *VMManager) SetRemoteClient(remoteClient client.Client) {
	m.remoteClient = remoteClient
}

// vmBuildParams contains parameters for building the VirtualMachine spec
type vmBuildParams struct {
	vmName               string
	machine              *vitistackv1alpha1.Machine
	disks                []kubevirtv1.Disk
	volumes              []kubevirtv1.Volume
	memoryRequest        string
	coresRequest         uint32
	socketsRequest       uint32
	threadsRequest       uint32
	networkConfiguration *kubevirtv1.Network
	networkBootOrder     *uint
	macAddress           string
}

// buildVMSpec creates the VirtualMachine specification from the given parameters
func (m *VMManager) buildVMSpec(ctx context.Context, params *vmBuildParams) *kubevirtv1.VirtualMachine {
	runStrategy := kubevirtv1.RunStrategyAlways
	cpuModel := viper.GetString(consts.CPU_MODEL)

	return &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      params.vmName,
			Namespace: params.machine.Namespace,
			Labels: map[string]string{
				vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
				"vitistack.io/source-machine":         params.machine.Name,
			},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy:         &runStrategy,
			DataVolumeTemplates: m.buildDataVolumeTemplates(ctx, params.machine, params.vmName),
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
						"vitistack.io/source-machine":         params.machine.Name,
					},
				},
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						CPU: &kubevirtv1.CPU{
							Model:   cpuModel,
							Cores:   params.coresRequest,
							Sockets: params.socketsRequest,
							Threads: params.threadsRequest,
						},
						Memory: &kubevirtv1.Memory{
							Guest: ptr.To(resource.MustParse(params.memoryRequest)),
						},
						Firmware: &kubevirtv1.Firmware{
							Bootloader: &kubevirtv1.Bootloader{
								EFI: &kubevirtv1.EFI{
									// SecureBoot disabled for broader OS compatibility
									SecureBoot: ptr.To(false),
								},
							},
						},
						Devices: kubevirtv1.Devices{
							Disks:                  params.disks,
							AutoattachPodInterface: ptr.To(false),
							Interfaces: []kubevirtv1.Interface{
								{
									Name:  params.networkConfiguration.Name,
									Model: kubevirtv1.VirtIO,
									InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{
										Bridge: &kubevirtv1.InterfaceBridge{},
									},
									BootOrder:  params.networkBootOrder,
									MacAddress: params.macAddress,
								},
							},
						},
					},
					Networks: []kubevirtv1.Network{*params.networkConfiguration},
					Volumes:  params.volumes,
				},
			},
		},
	}
}

// CreateVirtualMachine creates a KubeVirt VirtualMachine with the specified disks and volumes
func (m *VMManager) CreateVirtualMachine(
	ctx context.Context,
	machine *vitistackv1alpha1.Machine, vmName string,
	pvcNames []string) (*kubevirtv1.VirtualMachine, error) {
	logger := log.FromContext(ctx)

	// Build disks and volumes from the disk specs
	disks, volumes := m.buildDisksAndVolumes(machine, pvcNames)

	// Add boot source (ISO) if specified
	if machine.Annotations[AnnotationBootSource] == BootSourceDataVolume && machine.Spec.OS.ImageID != "" {
		disks, volumes = m.addISOBootSource(disks, volumes, vmName)
	}

	// Calculate resource requirements (validates MachineClass from supervisor cluster)
	memoryRequest, coresRequest, socketsRequest, threadsRequest, resourceErr := m.calculateResourceRequirements(ctx, machine)
	if resourceErr != nil {
		return nil, resourceErr
	}

	networkConfiguration, netErr := m.NetworkManager.GetOrCreateNetworkConfiguration(ctx, machine, m.remoteClient)
	if netErr != nil {
		m.recordNetworkFailure(ctx, machine, netErr)
		return nil, netErr
	}

	// Only set network boot order for PXE boot (when there's no imageID)
	var networkBootOrder *uint
	if machine.Spec.OS.ImageID == "" {
		bootOrder := uint(2)
		networkBootOrder = &bootOrder
	}

	macAddress, err := m.MacGenerator.GetMACAddress()
	if err != nil {
		return nil, err
	}

	if err := m.persistMacAddressesToNetworkConfiguration(ctx, machine, macAddress, networkConfiguration.Name); err != nil {
		logger.Error(err, "Failed to persist MAC address to NetworkConfiguration")
		return nil, err
	}

	vm := m.buildVMSpec(ctx, &vmBuildParams{
		vmName:               vmName,
		machine:              machine,
		disks:                disks,
		volumes:              volumes,
		memoryRequest:        memoryRequest,
		coresRequest:         coresRequest,
		socketsRequest:       socketsRequest,
		threadsRequest:       threadsRequest,
		networkConfiguration: networkConfiguration,
		networkBootOrder:     networkBootOrder,
		macAddress:           macAddress,
	})

	machine.Status.Phase = vitistackv1alpha1.MachinePhaseCreating
	machine.Status.State = consts.MachineStatePending

	// Note: We do NOT set Machine as the owner reference for the VirtualMachine because
	// they exist in different clusters (Machine on supervisor, VM on remote KubeVirt cluster).
	// Cross-cluster owner references are not supported in Kubernetes.

	if err := m.remoteClient.Create(ctx, vm); err != nil {
		return nil, err
	}

	logger.Info("Successfully created VirtualMachine", "virtualmachine", vm.Name, "disks", len(disks), "volumes", len(volumes))
	return vm, nil
}
