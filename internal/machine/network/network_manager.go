package network

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

// EventsManager handles event monitoring and error detection
type NetworkManager struct {
	client.Client
}

// NewManager creates a new events manager
func NewManager(c client.Client) *NetworkManager {
	return &NetworkManager{
		Client: c,
	}
}

// NetworkAttachmentConfig represents the CNI configuration for a NetworkAttachmentDefinition
type NetworkAttachmentConfig struct {
	CNIVersion string                          `json:"cniVersion"`
	Name       string                          `json:"name"`
	Plugins    []NetworkAttachmentConfigPlugin `json:"plugins"`
}

// NetworkAttachmentConfigPlugin represents a plugin in the CNI configuration
type NetworkAttachmentConfigPlugin struct {
	Type   string                 `json:"type"`
	Bridge string                 `json:"bridge,omitempty"`
	IPAM   map[string]interface{} `json:"ipam,omitempty"`
	VLAN   int                    `json:"vlan,omitempty"`
}

func (m *NetworkManager) GetOrCreateNetworkConfiguration(ctx context.Context, machine *vitistackv1alpha1.Machine, remoteClient client.Client) (*kubevirtv1.Network, error) {
	logger := log.FromContext(ctx)

	if machine == nil {
		return nil, fmt.Errorf("machine is nil")
	}

	// Get NetworkNamespace from supervisor cluster
	networkNamespace, err := m.findNetworkNamespace(ctx, machine)
	if err != nil {
		return nil, err
	}

	// If no NetworkNamespace found or vlanId not set, use default pod network
	if networkNamespace == nil || networkNamespace.Status.VlanID == 0 {
		logger.Info("Using default pod network",
			"namespace", machine.Namespace,
			"reason", "no NetworkNamespace or vlanId not set")
		return defaultPodNetwork(), nil
	}

	vlanId := networkNamespace.Status.VlanID
	nadName := fmt.Sprintf("vlan%d", vlanId)

	logger.Info("Found NetworkNamespace with vlanId",
		"namespace", machine.Namespace,
		"vlanId", vlanId,
		"nadName", nadName)

	// Ensure NetworkAttachmentDefinition exists on remote cluster
	if err := m.ensureNetworkAttachmentDefinition(ctx, nadName, machine.Namespace, vlanId, remoteClient); err != nil {
		return nil, err
	}

	// Return the network configuration
	return &kubevirtv1.Network{
		Name: nadName,
		NetworkSource: kubevirtv1.NetworkSource{
			Multus: &kubevirtv1.MultusNetwork{NetworkName: nadName},
		},
	}, nil
}

// findNetworkNamespace retrieves a NetworkNamespace for the machine
// TODO: Once Machine/KubernetesCluster CRD has a networkNamespaceName field, use that instead
// For now, try to get NetworkNamespace by machine name, then fall back to listing
func (m *NetworkManager) findNetworkNamespace(ctx context.Context, machine *vitistackv1alpha1.Machine) (*vitistackv1alpha1.NetworkNamespace, error) {
	logger := log.FromContext(ctx)

	// Try to get NetworkNamespace by machine name first
	networkNamespace := &vitistackv1alpha1.NetworkNamespace{}
	err := m.Get(ctx, client.ObjectKey{
		Name:      machine.Name,
		Namespace: machine.Namespace,
	}, networkNamespace)

	if err == nil {
		return networkNamespace, nil
	}

	if !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get NetworkNamespace from supervisor cluster: %w", err)
	}

	// NetworkNamespace not found by name, try to list and get the first one
	logger.Info("NetworkNamespace not found by name, searching for first available NetworkNamespace in namespace",
		"namespace", machine.Namespace)

	networkNamespaceList := &vitistackv1alpha1.NetworkNamespaceList{}
	if err := m.List(ctx, networkNamespaceList, client.InNamespace(machine.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list NetworkNamespaces from supervisor cluster: %w", err)
	}

	if len(networkNamespaceList.Items) == 0 {
		logger.Info("No NetworkNamespaces found in supervisor cluster",
			"namespace", machine.Namespace)
		return nil, nil
	}

	// Use the first NetworkNamespace found
	networkNamespace = &networkNamespaceList.Items[0]
	logger.Info("Using first available NetworkNamespace",
		"namespace", machine.Namespace,
		"networkNamespaceName", networkNamespace.Name)

	return networkNamespace, nil
}

// ensureNetworkAttachmentDefinition checks if a NetworkAttachmentDefinition exists and creates it if needed
func (m *NetworkManager) ensureNetworkAttachmentDefinition(ctx context.Context, nadName, namespace string, vlanId int, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	// Check if NetworkAttachmentDefinition already exists on remote cluster
	existingNAD := &netattdefv1.NetworkAttachmentDefinition{}
	err := remoteClient.Get(ctx, client.ObjectKey{
		Name:      nadName,
		Namespace: namespace,
	}, existingNAD)

	if err == nil {
		logger.Info("NetworkAttachmentDefinition already exists on remote cluster",
			"name", nadName,
			"namespace", namespace)
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check NetworkAttachmentDefinition on remote cluster: %w", err)
	}

	// Create NetworkAttachmentDefinition on remote cluster
	logger.Info("Creating NetworkAttachmentDefinition on remote cluster",
		"name", nadName,
		"namespace", namespace,
		"vlanId", vlanId)

	nad, err := m.createNetworkAttachmentDefinition(ctx, nadName, namespace, vlanId, remoteClient)
	if err != nil {
		return fmt.Errorf("failed to create NetworkAttachmentDefinition: %w", err)
	}

	logger.Info("Successfully created NetworkAttachmentDefinition",
		"name", nad.Name,
		"namespace", nad.Namespace,
		"vlanId", vlanId)

	return nil
}

// createNetworkAttachmentDefinition creates a NetworkAttachmentDefinition with the specified VLAN ID
func (m *NetworkManager) createNetworkAttachmentDefinition(ctx context.Context, name, namespace string, vlanId int, remoteClient client.Client) (*netattdefv1.NetworkAttachmentDefinition, error) {
	// Get CNI version from configuration (defaults to "1.0.0")
	cniVersion := viper.GetString(consts.CNI_VERSION)
	if cniVersion == "" {
		cniVersion = "1.0.0" // fallback if not set
	}

	// Create the CNI configuration
	config := NetworkAttachmentConfig{
		CNIVersion: cniVersion,
		Name:       "br0",
		Plugins: []NetworkAttachmentConfigPlugin{
			{
				Type:   "bridge",
				Bridge: "br0",
				IPAM:   map[string]any{},
				VLAN:   vlanId,
			},
			{
				Type: "tuning",
			},
		},
	}

	// Marshal the configuration to JSON
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal NAD config: %w", err)
	}

	// Create the NetworkAttachmentDefinition
	nad := &netattdefv1.NetworkAttachmentDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: netattdefv1.NetworkAttachmentDefinitionSpec{
			Config: string(configJSON),
		},
	}

	// Create on remote cluster
	if err := remoteClient.Create(ctx, nad); err != nil {
		return nil, fmt.Errorf("failed to create NetworkAttachmentDefinition on remote cluster: %w", err)
	}

	return nad, nil
}

// defaultPodNetwork returns the default pod network configuration
func defaultPodNetwork() *kubevirtv1.Network {
	return &kubevirtv1.Network{
		Name: "default",
		NetworkSource: kubevirtv1.NetworkSource{
			Pod: &kubevirtv1.PodNetwork{},
		},
	}
}

// CleanupNetworkAttachmentDefinition removes NADs that are no longer in use
// It checks if any VirtualMachines in the namespace are still using the NAD before deletion
func (m *NetworkManager) CleanupNetworkAttachmentDefinition(ctx context.Context, nadName, namespace string, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	// Check if the NAD exists
	nad := &netattdefv1.NetworkAttachmentDefinition{}
	err := remoteClient.Get(ctx, client.ObjectKey{
		Name:      nadName,
		Namespace: namespace,
	}, nad)

	if err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("NetworkAttachmentDefinition not found, nothing to cleanup",
				"name", nadName,
				"namespace", namespace)
			return nil
		}
		return fmt.Errorf("failed to get NetworkAttachmentDefinition: %w", err)
	}

	// List all VirtualMachines in the namespace to check if any are using this NAD
	vmList := &kubevirtv1.VirtualMachineList{}
	if err := remoteClient.List(ctx, vmList, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("failed to list VirtualMachines in namespace: %w", err)
	}

	// Check if any VM is using this NAD
	for i := range vmList.Items {
		vm := &vmList.Items[i]
		// Skip VMs that are being deleted (have a deletion timestamp)
		if vm.GetDeletionTimestamp() != nil {
			logger.V(1).Info("Skipping VM with deletion timestamp",
				"vm", vm.Name,
				"namespace", namespace)
			continue
		}
		if isVMUsingNAD(vm, nadName) {
			logger.Info("NetworkAttachmentDefinition is still in use by VirtualMachine, skipping deletion",
				"nad", nadName,
				"namespace", namespace,
				"vm", vm.Name)
			return nil
		}
	}

	// No VMs are using this NAD, safe to delete
	logger.Info("No VirtualMachines using NetworkAttachmentDefinition, deleting",
		"name", nadName,
		"namespace", namespace)

	if err := remoteClient.Delete(ctx, nad); err != nil {
		if errors.IsNotFound(err) {
			// Already deleted, that's fine
			return nil
		}
		return fmt.Errorf("failed to delete NetworkAttachmentDefinition: %w", err)
	}

	logger.Info("Successfully deleted NetworkAttachmentDefinition",
		"name", nadName,
		"namespace", namespace)

	return nil
}

// isVMUsingNAD checks if a VirtualMachine is using the specified NetworkAttachmentDefinition
func isVMUsingNAD(vm *kubevirtv1.VirtualMachine, nadName string) bool {
	if vm.Spec.Template == nil {
		return false
	}

	// Check all networks in the VM template
	for i := range vm.Spec.Template.Spec.Networks {
		network := &vm.Spec.Template.Spec.Networks[i]
		if network.Multus != nil && network.Multus.NetworkName == nadName {
			return true
		}
	}

	return false
}
