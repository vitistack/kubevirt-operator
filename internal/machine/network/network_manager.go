package network

import (
	"context"
	"fmt"

	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
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

func (m *NetworkManager) GetNetworkConfiguration(ctx context.Context, machine *vitistackv1alpha1.Machine, remoteClient client.Client) (*kubevirtv1.Network, error) {
	logger := log.FromContext(ctx)

	if machine == nil {
		return nil, fmt.Errorf("machine is nil")
	}

	// Attempt to list NetworkAttachmentDefinitions in the Machine namespace from the remote KubeVirt cluster
	nadList := &netattdefv1.NetworkAttachmentDefinitionList{}
	if err := remoteClient.List(ctx, nadList, client.InNamespace(machine.Namespace)); err != nil {
		logger.Error(err, "failed to list NetworkAttachmentDefinitions from remote cluster")
		// Fallback to default pod network if listing fails (e.g. CRD not installed / RBAC)
		return defaultPodNetwork(), nil
	}

	if len(nadList.Items) == 0 {
		logger.Info("No NetworkAttachmentDefinitions found, using default pod network", "namespace", machine.Namespace)
		return defaultPodNetwork(), nil
	}

	logger.Info("Found NetworkAttachmentDefinitions", "count", len(nadList.Items), "namespace", machine.Namespace)
	for i := range nadList.Items {
		nad := &nadList.Items[i]
		logger.Info("Evaluating NAD", "name", nad.Name, "namespace", nad.Namespace)
		if nad.Namespace == machine.Namespace { // first match in same namespace
			network := &kubevirtv1.Network{
				Name: nad.Name,
				NetworkSource: kubevirtv1.NetworkSource{
					Multus: &kubevirtv1.MultusNetwork{NetworkName: nad.Name},
				},
			}
			return network, nil
		}
	}

	// No matching NAD in namespace, fallback to pod network to avoid hard failure
	logger.Info("No matching NAD in namespace after evaluation, falling back to pod network", "namespace", machine.Namespace)
	return defaultPodNetwork(), nil
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
