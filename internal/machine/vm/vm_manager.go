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
	"fmt"

	"github.com/spf13/viper"
	"github.com/vitistack/crds/pkg/unstructuredutil"
	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	"github.com/vitistack/kubevirt-operator/internal/machine/network"
	"github.com/vitistack/kubevirt-operator/pkg/macaddress"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// VMManager handles VirtualMachine-related operations
type VMManager struct {
	client.Client
	Scheme         *runtime.Scheme
	NetworkManager *network.NetworkManager
	MacGenerator   macaddress.MacAddressGenerator
}

// NewManager creates a new VM manager
func NewManager(c client.Client, scheme *runtime.Scheme, macGenerator macaddress.MacAddressGenerator) *VMManager {
	return &VMManager{
		Client:       c,
		Scheme:       scheme,
		MacGenerator: macGenerator,
	}
}

// CreateVirtualMachine creates a KubeVirt VirtualMachine with the specified disks and volumes
func (m *VMManager) CreateVirtualMachine(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string, pvcNames []string, networkConfiguration *kubevirtv1.Network) (*kubevirtv1.VirtualMachine, error) {
	logger := log.FromContext(ctx)

	// Build disks and volumes from the disk specs
	disks, volumes := m.buildDisksAndVolumes(machine, pvcNames)

	// Calculate resource requirements
	memoryRequest, cpuRequest := m.calculateResourceRequirements(machine)

	// Create a local variable for RunStrategy since we need its address
	// Using RunStrategyAlways to ensure the VM starts automatically (replaces deprecated Running: true)
	runStrategy := kubevirtv1.RunStrategyAlways
	cpuModel := viper.GetString(consts.CPU_MODEL)

	networkBootOrder := uint(2)
	macAddress, err := m.MacGenerator.GetMACAddress()
	if err != nil {
		return nil, err
	}

	if err := m.persistMacAddressesToNetworkConfiguration(ctx, machine, macAddress, networkConfiguration.Name); err != nil {
		logger.Error(err, "Failed to persist MAC address to NetworkConfiguration")
		return nil, err
	}

	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: machine.Namespace,
			Labels: map[string]string{
				"managed-by":     "kubevirt-operator",
				"source-machine": machine.Name,
			},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &runStrategy, // Use modern RunStrategy instead of deprecated Running field
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"managed-by":     "kubevirt-operator",
						"source-machine": machine.Name,
					},
				},
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						Resources: kubevirtv1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse(memoryRequest),
								corev1.ResourceCPU:    resource.MustParse(cpuRequest),
							},
						},
						CPU: &kubevirtv1.CPU{
							Model: cpuModel,
						},
						Devices: kubevirtv1.Devices{
							Disks: disks,
							Interfaces: []kubevirtv1.Interface{
								{
									Name:  networkConfiguration.Name,
									Model: kubevirtv1.VirtIO,
									InterfaceBindingMethod: kubevirtv1.InterfaceBindingMethod{
										Bridge: &kubevirtv1.InterfaceBridge{},
									},
									BootOrder:  &networkBootOrder,
									MacAddress: macAddress,
								},
							},
						},
					},
					Networks: []kubevirtv1.Network{
						*networkConfiguration,
					},
					Volumes: volumes,
				},
			},
		},
	}

	machine.Status.Phase = vitistackv1alpha1.MachinePhaseCreating
	machine.Status.State = consts.MachineStatePending

	// Set Machine as the owner of the VirtualMachine
	if err := controllerutil.SetControllerReference(machine, vm, m.Scheme); err != nil {
		return nil, err
	}

	if err := m.Create(ctx, vm); err != nil {
		return nil, err
	}

	logger.Info("Successfully created VirtualMachine", "virtualmachine", vm.Name, "disks", len(disks), "volumes", len(volumes))
	return vm, nil
}

// calculateResourceRequirements determines CPU and memory requirements based on machine spec
func (m *VMManager) calculateResourceRequirements(machine *vitistackv1alpha1.Machine) (memoryRequest, cpuRequest string) {
	// Default resource values based on machine spec
	oneGi := "1Gi"
	memoryRequest = oneGi
	cpuRequest = "1"

	// Use simple defaults based on machine type from the external CRD
	if machine.Spec.InstanceType != "" {
		switch machine.Spec.InstanceType {
		case "small":
			memoryRequest = oneGi
			cpuRequest = "1"
		case "medium":
			memoryRequest = "2Gi"
			cpuRequest = "2"
		case "large":
			memoryRequest = "4Gi"
			cpuRequest = "4"
		default:
			memoryRequest = oneGi
			cpuRequest = "1"
		}
	}

	// Override with spec values if provided
	if machine.Spec.Memory > 0 {
		memoryRequest = fmt.Sprintf("%dMi", machine.Spec.Memory/1024/1024) // Convert bytes to MiB
	}
	if machine.Spec.CPU.Cores > 0 {
		cpuRequest = fmt.Sprintf("%d", machine.Spec.CPU.Cores)
	}

	return memoryRequest, cpuRequest
}

// buildDisksAndVolumes creates disk and volume specifications for the VM
func (m *VMManager) buildDisksAndVolumes(machine *vitistackv1alpha1.Machine, pvcNames []string) ([]kubevirtv1.Disk, []kubevirtv1.Volume) {
	l := len(machine.Spec.Disks)
	// Always allocate with capacity (>=1 for default root case)
	capSize := l
	if capSize == 0 {
		capSize = 1
	}
	disks := make([]kubevirtv1.Disk, 0, capSize)
	volumes := make([]kubevirtv1.Volume, 0, capSize)

	bootorder := uint(1) // Default boot order for the first disk

	// If no disks are specified in the spec, create a default root disk
	if len(machine.Spec.Disks) == 0 {
		disks = append(disks, kubevirtv1.Disk{
			Name: "root",
			DiskDevice: kubevirtv1.DiskDevice{
				Disk: &kubevirtv1.DiskTarget{
					Bus: "virtio",
				},
			},
			BootOrder: &bootorder,
		})

		volumes = append(volumes, kubevirtv1.Volume{
			Name: "root",
			VolumeSource: kubevirtv1.VolumeSource{
				PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{
					PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcNames[0],
					},
				},
			},
		})

		return disks, volumes
	}

	// Create disks and volumes based on machine.spec.disks
	for i, diskSpec := range machine.Spec.Disks {
		diskName := "root"
		if diskSpec.Name != "" {
			diskName = diskSpec.Name
		} else if i > 0 {
			diskName = fmt.Sprintf("disk%d", i)
		}

		// Determine bus type (virtio is generally preferred for performance)
		var busType kubevirtv1.DiskBus = "virtio"
		if diskSpec.Type != "" {
			// Map disk types to appropriate bus types
			switch diskSpec.Type { // TODO: extend mapping if new types introduced
			case "nvme":
				busType = "virtio" // KubeVirt uses virtio for NVMe-style performance
			case "sata":
				busType = "sata"
			case "scsi":
				busType = "scsi"
			default:
				busType = "virtio"
			}
		}

		disk := kubevirtv1.Disk{
			Name: diskName,
			DiskDevice: kubevirtv1.DiskDevice{
				Disk: &kubevirtv1.DiskTarget{
					Bus: busType,
				},
			},
		}

		// Set boot order for boot disk
		if diskSpec.Boot {
			bootOrder := uint(1)
			disk.BootOrder = &bootOrder
		}

		disks = append(disks, disk)

		volume := kubevirtv1.Volume{
			Name: diskName,
			VolumeSource: kubevirtv1.VolumeSource{
				PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{
					PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcNames[i],
					},
				},
			},
		}

		volumes = append(volumes, volume)
	}

	return disks, volumes
}

// persistMacAddressesToNetworkConfiguration creates or updates a NetworkConfiguration CRD with the MAC address
func (m *VMManager) persistMacAddressesToNetworkConfiguration(ctx context.Context, machine *vitistackv1alpha1.Machine, macAddress, networkName string) error {
	logger := log.FromContext(ctx)

	// Create network interface entry
	networkInterface := vitistackv1alpha1.NetworkConfigurationInterface{
		Name:       networkName,
		MacAddress: macAddress,
	}

	// Try to get existing NetworkConfiguration as unstructured
	unstructuredNetConfig := &unstructured.Unstructured{}
	unstructuredNetConfig.SetGroupVersionKind(vitistackv1alpha1.GroupVersion.WithKind("NetworkConfiguration"))

	err := m.Get(ctx, client.ObjectKey{Name: machine.Name, Namespace: machine.Namespace}, unstructuredNetConfig)

	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get NetworkConfiguration: %w", err)
		}

		// Create new NetworkConfiguration as typed object
		netConfig := &vitistackv1alpha1.NetworkConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
				Labels: map[string]string{
					"managed-by":     "kubevirt-operator",
					"source-machine": machine.Name,
				},
			},
			Spec: vitistackv1alpha1.NetworkConfigurationSpec{
				Name:        machine.Name,
				Description: "Network configuration for machine " + machine.Name,
				NetworkInterfaces: []vitistackv1alpha1.NetworkConfigurationInterface{
					networkInterface,
				},
			},
		}

		// Convert to unstructured for creation
		unstructuredNew, err := unstructuredutil.NetworkConfigurationToUnstructured(netConfig)
		if err != nil {
			return fmt.Errorf("failed to convert NetworkConfiguration to unstructured: %w", err)
		}

		// Ensure GVK is set explicitly
		unstructuredNew.SetGroupVersionKind(vitistackv1alpha1.GroupVersion.WithKind("NetworkConfiguration"))

		// Set Machine as the owner of the NetworkConfiguration (on unstructured object)
		if err := controllerutil.SetControllerReference(machine, unstructuredNew, m.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference: %w", err)
		}

		if err := m.Create(ctx, unstructuredNew); err != nil {
			return fmt.Errorf("failed to create NetworkConfiguration: %w", err)
		}

		logger.Info("Created NetworkConfiguration", "name", netConfig.Name, "macAddress", macAddress)
		return nil
	}

	// Convert unstructured to typed NetworkConfiguration
	existingNetConfig, err := unstructuredutil.NetworkConfigurationFromUnstructured(unstructuredNetConfig)
	if err != nil {
		return fmt.Errorf("failed to convert unstructured to NetworkConfiguration: %w", err)
	}

	// Update existing NetworkConfiguration
	// Check if interface with this MAC already exists
	interfaceExists := false
	for i := range existingNetConfig.Spec.NetworkInterfaces {
		iface := &existingNetConfig.Spec.NetworkInterfaces[i]
		if iface.Name == networkName {
			iface.MacAddress = macAddress
			interfaceExists = true
			break
		}
	}

	// If interface doesn't exist, append it
	if !interfaceExists {
		existingNetConfig.Spec.NetworkInterfaces = append(existingNetConfig.Spec.NetworkInterfaces, networkInterface)
	}

	// Convert back to unstructured for update
	unstructuredUpdated, err := unstructuredutil.NetworkConfigurationToUnstructured(existingNetConfig)
	if err != nil {
		return fmt.Errorf("failed to convert updated NetworkConfiguration to unstructured: %w", err)
	}

	// Ensure GVK is set explicitly
	unstructuredUpdated.SetGroupVersionKind(vitistackv1alpha1.GroupVersion.WithKind("NetworkConfiguration"))

	if err := m.Update(ctx, unstructuredUpdated); err != nil {
		return fmt.Errorf("failed to update NetworkConfiguration: %w", err)
	}

	logger.Info("Updated NetworkConfiguration", "name", existingNetConfig.Name, "macAddress", macAddress)
	return nil
}
