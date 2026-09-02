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
	"github.com/vitistack/kubevirt-operator/internal/machine/storage"
	"github.com/vitistack/kubevirt-operator/pkg/macaddress"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// LabelSourceMachine is the label key set on cluster-managed objects (VMs,
// DataVolumeTemplates, NetworkConfigurations, ...) pointing back to the
// originating Machine resource by name.
const LabelSourceMachine = "vitistack.io/source-machine"

const (
	PolicySpreadContraints = "spreadconstraint"
	PolicyAntiAffinity     = "antiaffinity"
)

// VMManager handles VirtualMachine-related operations
type VMManager struct {
	supervisorClient client.Client // Client for supervisor cluster (Machine CRDs)
	remoteClient     client.Client // Client for remote KubeVirt cluster (VMs)
	Scheme           *runtime.Scheme
	NetworkManager   *network.NetworkManager
	MacGenerator     macaddress.MacAddressGenerator
	StatusManager    *status.StatusManager
	StorageManager   *storage.StorageManager
}

// NewManager creates a new VM manager
// The client parameter is the supervisor cluster client
func NewManager(c client.Client, scheme *runtime.Scheme, macGenerator macaddress.MacAddressGenerator, statusManager *status.StatusManager, storageManager *storage.StorageManager) *VMManager {
	return &VMManager{
		supervisorClient: c,
		remoteClient:     c, // Default to supervisor client for backward compatibility
		Scheme:           scheme,
		MacGenerator:     macGenerator,
		StatusManager:    statusManager,
		StorageManager:   storageManager,
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

	cpu := &kubevirtv1.CPU{
		Model:   cpuModel,
		Cores:   params.coresRequest,
		Sockets: params.socketsRequest,
		Threads: params.threadsRequest,
	}
	// Workaround for a libvirt host-model regression (KubeVirt >= v1.8.0): the
	// expanded host-model CPU requires the "ht" (HTT) CPUID flag, which QEMU does
	// not expose for the guest topology, so check='full' validation fails with
	// "guest CPU doesn't match specification: extra features: ht" and the VM never
	// starts. Disabling the feature drops that requirement. Enabled by default,
	// opt out per cluster via DISABLE_CPU_HT_FEATURE; only host-model exhibits the
	// regression.
	if cpuModel == "host-model" && viper.GetBool(consts.DISABLE_CPU_HT_FEATURE) {
		cpu.Features = append(cpu.Features, kubevirtv1.CPUFeature{
			Name:   "ht",
			Policy: "disable",
		})
	}

	evictionStrategy := kubevirtv1.EvictionStrategyLiveMigrateIfPossible

	return &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      params.vmName,
			Namespace: params.machine.Namespace,
			Labels: map[string]string{
				vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
				LabelSourceMachine:                    params.machine.Name,
			},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy:         &runStrategy,
			DataVolumeTemplates: m.buildDataVolumeTemplates(ctx, params.machine, params.vmName),
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
						LabelSourceMachine:                    params.machine.Name,
					},
				},
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					EvictionStrategy: &evictionStrategy,
					Domain: kubevirtv1.DomainSpec{
						CPU: cpu,
						Memory: &kubevirtv1.Memory{
							Guest: new(resource.MustParse(params.memoryRequest)),
						},
						Firmware: &kubevirtv1.Firmware{
							Bootloader: &kubevirtv1.Bootloader{
								EFI: &kubevirtv1.EFI{
									// SecureBoot disabled for broader OS compatibility
									SecureBoot: new(false),
								},
							},
						},
						Devices: kubevirtv1.Devices{
							Rng:                    &kubevirtv1.Rng{},
							Disks:                  params.disks,
							AutoattachPodInterface: new(false),
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
func (m *VMManager) CreateVirtualMachine(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string, pvcNames []string) (*kubevirtv1.VirtualMachine, error) {
	logger := log.FromContext(ctx)

	// Build disks and volumes from the disk specs
	disks, volumes := m.buildDisksAndVolumes(machine, pvcNames)

	// Add boot source (ISO) if specified
	if machine.Annotations[AnnotationBootSource] == BootSourceDataVolume && machine.Spec.OS.ImageID != "" {
		disks, volumes = m.addISOBootSource(disks, volumes, machine, vmName)
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

	macAddress, err := m.resolveOrGenerateMAC(ctx, machine, networkConfiguration.Name)
	if err != nil {
		return nil, err
	}

	if err := m.persistMacAddressesToNetworkConfiguration(ctx, machine, macAddress, networkConfiguration.Name); err != nil {
		logger.Error(err, "Failed to persist MAC address to NetworkConfiguration")
		return nil, err
	}

	// Resolve cloud-init (cidata) after the MAC is persisted — the rendered
	// network-config depends on static-ip-operator having allocated an IP for
	// that MAC. If it hasn't yet, resolveCloudInit returns ErrWaitingForStaticIP
	// and the reconciler requeues.
	ciBundle, err := m.resolveCloudInit(ctx, machine)
	if err != nil {
		return nil, err
	}
	if ciBundle != nil {
		disks, volumes = addCloudInitDisk(disks, volumes, ciBundle)
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

	// TODO: make configurable via values.yaml
	switch viper.GetString(consts.PLACEMENT_POLICY) {
	case PolicyAntiAffinity:
		vm = addAntiAffinity(machine, vm)
	case PolicySpreadContraints:
		vm = addSpreadConstraints(machine, vm)
	default:

	}

	if viper.GetBool(consts.DESCHEDULER_ANNOTATION) {
		vm = addDeschedulerAnnotation(vm)
	}

	// Note: We do NOT set Machine as the owner reference for the VirtualMachine because
	// they exist in different clusters (Machine on supervisor, VM on remote KubeVirt cluster).
	// Cross-cluster owner references are not supported in Kubernetes.

	if err := m.remoteClient.Create(ctx, vm); err != nil {
		return nil, err
	}

	logger.Info("Successfully created VirtualMachine", "virtualmachine", vm.Name, "disks", len(disks), "volumes", len(volumes))
	return vm, nil
}

func addSpreadConstraints(m *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) *kubevirtv1.VirtualMachine {
	clusterID := m.Labels[vitistackv1alpha1.ClusterIdAnnotation]
	noderole := m.Labels[vitistackv1alpha1.NodeRoleAnnotation]

	// copy labels from Machine to VirtualMachine, these are used to select VMs using LabelSelector and go in the ObjectMeta.
	for key, value := range m.Labels {
		if _, found := vm.Spec.Template.ObjectMeta.Labels[key]; !found {
			vm.Spec.Template.ObjectMeta.Labels[key] = value
		}
	}

	// Spread VMs of this cluster+role across nodes so one node failure can't
	// take out multiple instances of the same role.
	spreadConstraints := []corev1.TopologySpreadConstraint{
		{
			MaxSkew:            1,
			TopologyKey:        corev1.LabelHostname,
			WhenUnsatisfiable:  corev1.ScheduleAnyway,
			MinDomains:         new(int32(1)),
			NodeAffinityPolicy: new(corev1.NodeInclusionPolicyHonor),
			NodeTaintsPolicy:   new(corev1.NodeInclusionPolicyHonor),
			LabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					vitistackv1alpha1.ClusterIdAnnotation: clusterID,
					vitistackv1alpha1.NodeRoleAnnotation:  noderole,
				},
			},
		},
	}

	vm.Spec.Template.Spec.TopologySpreadConstraints = spreadConstraints

	return vm
}

func addAntiAffinity(m *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) *kubevirtv1.VirtualMachine {
	clusterID := m.Labels[vitistackv1alpha1.ClusterIdAnnotation]
	noderole := m.Labels[vitistackv1alpha1.NodeRoleAnnotation]

	// copy labels from Machine to VirtualMachine, these are used to select VMs using LabelSelector and go in the ObjectMeta.
	for key, value := range m.Labels {
		if _, found := vm.Spec.Template.ObjectMeta.Labels[key]; !found {
			vm.Spec.Template.ObjectMeta.Labels[key] = value
		}
	}

	affinity := &corev1.Affinity{PodAntiAffinity: &corev1.PodAntiAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			Weight: 100,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						vitistackv1alpha1.ClusterIdAnnotation: clusterID,
						vitistackv1alpha1.NodeRoleAnnotation:  noderole,
					},
				},
				TopologyKey: corev1.LabelHostname,
			},
		}},
	}}

	vm.Spec.Template.Spec.Affinity = affinity

	return vm
}

func addDeschedulerAnnotation(vm *kubevirtv1.VirtualMachine) *kubevirtv1.VirtualMachine {
	vm.Spec.Template.ObjectMeta.Annotations["descheduler.alpha.kubernetes.io/evict"] = "true"
	return vm
}
