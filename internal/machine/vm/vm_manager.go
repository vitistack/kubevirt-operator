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
	"regexp"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	"github.com/vitistack/kubevirt-operator/internal/machine/network"
	"github.com/vitistack/kubevirt-operator/internal/machine/status"
	"github.com/vitistack/kubevirt-operator/pkg/macaddress"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

// CreateVirtualMachine creates a KubeVirt VirtualMachine with the specified disks and volumes
func (m *VMManager) CreateVirtualMachine(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string, pvcNames []string) (*kubevirtv1.VirtualMachine, error) {
	logger := log.FromContext(ctx)

	// Build disks and volumes from the disk specs
	disks, volumes := m.buildDisksAndVolumes(machine, pvcNames)

	// Calculate resource requirements
	memoryRequest, cpuRequest := m.calculateResourceRequirements(machine)

	networkConfiguration, netErr := m.NetworkManager.GetOrCreateNetworkConfiguration(ctx, machine, m.remoteClient)
	if netErr != nil {
		m.recordNetworkFailure(ctx, machine, netErr)
		return nil, netErr
	}

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
				"managed-by":     viper.GetString(consts.MANAGED_BY),
				"source-machine": machine.Name,
			},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &runStrategy, // Use modern RunStrategy instead of deprecated Running field
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"managed-by":     viper.GetString(consts.MANAGED_BY),
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

	// Note: We do NOT set Machine as the owner reference for the VirtualMachine because
	// they exist in different clusters (Machine on supervisor, VM on remote KubeVirt cluster).
	// Cross-cluster owner references are not supported in Kubernetes.
	// Instead, we rely on labels (source-machine, managed-by) for tracking and cleanup.

	// Create VM in the remote KubeVirt cluster
	if err := m.remoteClient.Create(ctx, vm); err != nil {
		return nil, err
	}

	logger.Info("Successfully created VirtualMachine", "virtualmachine", vm.Name, "disks", len(disks), "volumes", len(volumes))
	return vm, nil
}

// recordNetworkFailure updates machine status for network configuration errors.
func (m *VMManager) recordNetworkFailure(ctx context.Context, machine *vitistackv1alpha1.Machine, err error) {
	logger := log.FromContext(ctx)
	reason := "Failed to get network configuration in current namespace"
	errorTitle := "NetworkConfigurationError"
	logger.Error(err, reason)
	if machine.Status.Phase != vitistackv1alpha1.MachinePhaseFailed {
		machine.Status.Conditions = append(machine.Status.Conditions, vitistackv1alpha1.MachineCondition{
			Type:    vitistackv1alpha1.ConditionUnknown,
			Status:  string(metav1.ConditionFalse),
			Reason:  reason,
			Message: errorTitle,
		})
	}
	machine.Status.Phase = vitistackv1alpha1.MachinePhaseFailed
	machine.Status.LastUpdated = metav1.Now()
	machine.Status.FailureMessage = &errorTitle
	machine.Status.FailureReason = &reason
	if statusErr := m.StatusManager.UpdateMachineStatus(ctx, machine, errorTitle); statusErr != nil {
		logger.Error(statusErr, errorTitle)
	}
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

// generateShortNetworkConfigName generates a shortened name for NetworkConfiguration spec.name field
// to comply with the 32-byte limit. Follows patterns:
// - vm-<cluster>-wp<number> for workers (e.g., simple-cluster-worker-default-worker-pool-0 -> vm-simple-cluster-wp0)
// - vm-<cluster>-cp<number> for control planes (e.g., simple-cluster-control-plane-0 -> vm-simple-cluster-cp0)
func generateShortNetworkConfigName(machineName string) string {
	// Pattern 1: <cluster>-worker-default-worker-pool-<number> or <cluster>-worker-<pool>-pool-<number>
	// Extract cluster name and number, convert to vm-<cluster>-wp<number>
	workerPattern := regexp.MustCompile(`^(.+?)-worker(?:-default)?(?:-worker)?-pool-(\d+)$`)
	if matches := workerPattern.FindStringSubmatch(machineName); matches != nil {
		clusterName := matches[1]
		poolNumber := matches[2]
		shortName := fmt.Sprintf("vm-%s-wp%s", clusterName, poolNumber)
		if len(shortName) <= 32 {
			return shortName
		}
	}

	// Pattern 2: <cluster>-control-plane-<number>
	// Convert to vm-<cluster>-cp<number>
	controlPlanePattern := regexp.MustCompile(`^(.+?)-control-plane-(\d+)$`)
	if matches := controlPlanePattern.FindStringSubmatch(machineName); matches != nil {
		clusterName := matches[1]
		number := matches[2]
		shortName := fmt.Sprintf("vm-%s-cp%s", clusterName, number)
		if len(shortName) <= 32 {
			return shortName
		}
	}

	// Fallback: If patterns don't match or result is still too long,
	// use vm- prefix and truncate/hash the name
	shortName := "vm-" + machineName
	if len(shortName) <= 32 {
		return shortName
	}

	// Truncate to 32 bytes (keep vm- prefix and take first 29 chars of machine name)
	return "vm-" + machineName[:29]
}

// persistMacAddressesToNetworkConfiguration creates or updates a NetworkConfiguration CRD with the MAC address
func (m *VMManager) persistMacAddressesToNetworkConfiguration(ctx context.Context, machine *vitistackv1alpha1.Machine, macAddress, networkName string) error {
	logger := log.FromContext(ctx)

	// Create network interface entry
	networkInterface := vitistackv1alpha1.NetworkConfigurationInterface{
		Name:       networkName,
		MacAddress: macAddress,
	}

	// Try to get existing NetworkConfiguration using typed struct
	existingNetConfig := &vitistackv1alpha1.NetworkConfiguration{}
	err := m.supervisorClient.Get(ctx, client.ObjectKey{Name: machine.Name, Namespace: machine.Namespace}, existingNetConfig)

	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get NetworkConfiguration: %w", err)
		}

		// NetworkConfiguration doesn't exist, create a new one
		// Generate a shortened name for spec.name to comply with 32-byte limit
		shortName := generateShortNetworkConfigName(machine.Name)
		netConfig := &vitistackv1alpha1.NetworkConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
				Labels: map[string]string{
					"managed-by":     viper.GetString(consts.MANAGED_BY),
					"source-machine": machine.Name,
				},
			},
			Spec: vitistackv1alpha1.NetworkConfigurationSpec{
				Name:        shortName,
				Description: "Network configuration for machine " + machine.Name,
				NetworkInterfaces: []vitistackv1alpha1.NetworkConfigurationInterface{
					networkInterface,
				},
			},
		}

		// Set Machine as the owner of the NetworkConfiguration
		if err := controllerutil.SetControllerReference(machine, netConfig, m.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference: %w", err)
		}

		// Create in supervisor cluster
		if err := m.supervisorClient.Create(ctx, netConfig); err != nil {
			return fmt.Errorf("failed to create NetworkConfiguration: %w", err)
		}

		logger.Info("Created NetworkConfiguration", "name", netConfig.Name, "macAddress", macAddress)
		return nil
	}

	// NetworkConfiguration exists, update it
	// Check if interface with this network name already exists
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

	// Update in supervisor cluster
	if err := m.supervisorClient.Update(ctx, existingNetConfig); err != nil {
		return fmt.Errorf("failed to update NetworkConfiguration: %w", err)
	}

	logger.Info("Updated NetworkConfiguration", "name", existingNetConfig.Name, "macAddress", macAddress)
	return nil
}

// CleanupNetworkConfiguration deletes the NetworkConfiguration associated with the machine
func (m *VMManager) CleanupNetworkConfiguration(ctx context.Context, machine *vitistackv1alpha1.Machine) error {
	logger := log.FromContext(ctx)

	// Try to get existing NetworkConfiguration using typed struct
	netConfig := &vitistackv1alpha1.NetworkConfiguration{}
	err := m.supervisorClient.Get(ctx, client.ObjectKey{Name: machine.Name, Namespace: machine.Namespace}, netConfig)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return fmt.Errorf("failed to get NetworkConfiguration: %w", err)
		}
		// NetworkConfiguration doesn't exist, nothing to clean up
		logger.Info("NetworkConfiguration not found, nothing to clean up", "machine", machine.Name)
		return nil
	}

	// Validate labels match expected values
	labels := netConfig.GetLabels()
	expectedManagedBy := viper.GetString(consts.MANAGED_BY)
	if managedBy, ok := labels["managed-by"]; !ok || managedBy != expectedManagedBy {
		logger.Info("NetworkConfiguration has unexpected managed-by label, skipping deletion",
			"name", machine.Name,
			"managed-by", managedBy)
		return fmt.Errorf("NetworkConfiguration managed-by label mismatch: expected '%s', got '%s'", expectedManagedBy, managedBy)
	}

	if sourceMachine, ok := labels["source-machine"]; !ok || sourceMachine != machine.Name {
		logger.Info("NetworkConfiguration has unexpected source-machine label, skipping deletion",
			"name", machine.Name,
			"source-machine", sourceMachine,
			"expected", machine.Name)
		return fmt.Errorf("NetworkConfiguration source-machine label mismatch: expected '%s', got '%s'", machine.Name, sourceMachine)
	}

	// Validate MAC addresses match machine status if available
	if len(machine.Status.NetworkInterfaces) > 0 {
		// Build a map of MAC addresses from machine status
		statusMacs := make(map[string]bool)
		for _, iface := range machine.Status.NetworkInterfaces {
			if iface.MACAddress != "" {
				statusMacs[iface.MACAddress] = true
			}
		}

		// Check if NetworkConfiguration MAC addresses match
		for i := range netConfig.Spec.NetworkInterfaces {
			netIface := &netConfig.Spec.NetworkInterfaces[i]
			if netIface.MacAddress != "" {
				if !statusMacs[netIface.MacAddress] {
					logger.Info("NetworkConfiguration has MAC address not found in machine status",
						"name", machine.Name,
						"macAddress", netIface.MacAddress)
					return fmt.Errorf("NetworkConfiguration MAC address '%s' not found in machine status", netIface.MacAddress)
				}
			}
		}

		logger.Info("NetworkConfiguration validation passed", "name", machine.Name, "macAddresses", len(netConfig.Spec.NetworkInterfaces))
	} else {
		logger.Info("No network interfaces in machine status, skipping MAC address validation", "name", machine.Name)
	}

	// Delete the NetworkConfiguration from supervisor cluster
	if err := m.supervisorClient.Delete(ctx, netConfig); err != nil {
		return fmt.Errorf("failed to delete NetworkConfiguration: %w", err)
	}

	logger.Info("Successfully deleted NetworkConfiguration", "name", machine.Name)
	return nil
}
