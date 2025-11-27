package kubevirtconfigservice

import (
	"context"
	"fmt"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubevirtConfigService provides methods to fetch and manage KubevirtConfig resources
type KubevirtConfigService struct {
	client client.Client
}

// NewKubevirtConfigService creates a new KubevirtConfigService
func NewKubevirtConfigService(c client.Client) *KubevirtConfigService {
	return &KubevirtConfigService{
		client: c,
	}
}

// FetchAllKubevirtConfigs returns all KubevirtConfig resources in the configured namespace
func (s *KubevirtConfigService) FetchAllKubevirtConfigs(ctx context.Context, namespace string) ([]vitistackv1alpha1.KubevirtConfig, error) {
	kubevirtConfigList := &vitistackv1alpha1.KubevirtConfigList{}

	listOpts := []client.ListOption{}
	if namespace != "" {
		listOpts = append(listOpts, client.InNamespace(namespace))
	}

	if err := s.client.List(ctx, kubevirtConfigList, listOpts...); err != nil {
		return nil, fmt.Errorf("failed to list KubevirtConfigs: %w", err)
	}

	return kubevirtConfigList.Items, nil
}

// FetchKubevirtConfigByName returns a specific KubevirtConfig by name
func (s *KubevirtConfigService) FetchKubevirtConfigByName(ctx context.Context, name string, namespace string) (*vitistackv1alpha1.KubevirtConfig, error) {
	kubevirtConfig := &vitistackv1alpha1.KubevirtConfig{}

	key := client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}

	if err := s.client.Get(ctx, key, kubevirtConfig); err != nil {
		return nil, fmt.Errorf("failed to get KubevirtConfig %s: %w", name, err)
	}

	return kubevirtConfig, nil
}
