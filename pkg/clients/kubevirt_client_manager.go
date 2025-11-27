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

package clients

import (
	"context"
	"fmt"
	"sync"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// KubevirtClientManager manages multiple Kubernetes clients for different KubeVirt clusters
// It implements the ClientManager interface
type KubevirtClientManager struct {
	supervisorClient client.Client
	scheme           *runtime.Scheme

	mu      sync.RWMutex
	clients map[string]client.Client // map[kubevirtConfigName]client
}

// Ensure KubevirtClientManager implements ClientManager interface
var _ ClientManager = (*KubevirtClientManager)(nil)

// NewKubevirtClientManager creates a new client manager
func NewKubevirtClientManager(supervisorClient client.Client, scheme *runtime.Scheme) ClientManager {
	return &KubevirtClientManager{
		supervisorClient: supervisorClient,
		scheme:           scheme,
		clients:          make(map[string]client.Client),
	}
}

// GetClientForConfig returns a Kubernetes client for the specified KubevirtConfig
// The client is cached and reused for subsequent calls
func (m *KubevirtClientManager) GetClientForConfig(ctx context.Context, kubevirtConfigName string) (client.Client, error) {
	logger := log.FromContext(ctx)

	// Check if we already have a cached client
	m.mu.RLock()
	if cachedClient, exists := m.clients[kubevirtConfigName]; exists {
		m.mu.RUnlock()
		return cachedClient, nil
	}
	m.mu.RUnlock()

	// Fetch the KubevirtConfig CRD (cluster-scoped, no namespace)
	kubevirtConfig := &vitistackv1alpha1.KubevirtConfig{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{
		Name: kubevirtConfigName,
	}, kubevirtConfig); err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("KubevirtConfig %s not found", kubevirtConfigName)
		}
		return nil, fmt.Errorf("failed to get KubevirtConfig: %w", err)
	}

	// Get the kubeconfig secret (secrets are namespaced)
	secretName := kubevirtConfig.Spec.KubeconfigSecretRef
	if secretName == "" {
		return nil, fmt.Errorf("KubevirtConfig %s has no kubeconfigSecretRef", kubevirtConfigName)
	}

	secretNamespace := kubevirtConfig.Spec.SecretNamespace
	if secretNamespace == "" {
		return nil, fmt.Errorf("KubevirtConfig %s has no secretNamespace", kubevirtConfigName)
	}

	secret := &corev1.Secret{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig secret %s/%s: %w", secretNamespace, secretName, err)
	}

	// Extract kubeconfig from secret
	kubeconfigData, ok := secret.Data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain 'kubeconfig' key", secretNamespace, secretName)
	}

	// Create REST config from kubeconfig
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("failed to create REST config from kubeconfig: %w", err)
	}

	// Create a new Kubernetes client
	remoteClient, err := client.New(restConfig, client.Options{
		Scheme: m.scheme,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Cache the client
	m.mu.Lock()
	m.clients[kubevirtConfigName] = remoteClient
	m.mu.Unlock()

	logger.Info("Created and cached Kubernetes client for KubevirtConfig", "config", kubevirtConfigName)
	return remoteClient, nil
}

// ListKubevirtConfigs returns all KubevirtConfig CRDs (cluster-scoped)
func (m *KubevirtClientManager) ListKubevirtConfigs(ctx context.Context) ([]vitistackv1alpha1.KubevirtConfig, error) {
	kubevirtConfigList := &vitistackv1alpha1.KubevirtConfigList{}

	// KubevirtConfig is cluster-scoped, no namespace filter needed
	if err := m.supervisorClient.List(ctx, kubevirtConfigList); err != nil {
		return nil, fmt.Errorf("failed to list KubevirtConfigs: %w", err)
	}

	return kubevirtConfigList.Items, nil
}

// InvalidateClient removes a cached client, forcing it to be recreated on next access
func (m *KubevirtClientManager) InvalidateClient(kubevirtConfigName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.clients, kubevirtConfigName)
}

// InvalidateAll removes all cached clients
func (m *KubevirtClientManager) InvalidateAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clients = make(map[string]client.Client)
}

// GetSupervisorClient returns the Kubernetes client for the supervisor cluster
func (m *KubevirtClientManager) GetSupervisorClient() client.Client {
	return m.supervisorClient
}

// ValidateConnection tests the connection to a KubeVirt cluster
func (m *KubevirtClientManager) ValidateConnection(ctx context.Context, kubevirtConfigName string) error {
	remoteClient, err := m.GetClientForConfig(ctx, kubevirtConfigName)
	if err != nil {
		return err
	}

	// Try to list namespaces as a connectivity test
	namespaceList := &corev1.NamespaceList{}
	if err := remoteClient.List(ctx, namespaceList, client.Limit(1)); err != nil {
		return fmt.Errorf("failed to validate connection to cluster: %w", err)
	}

	return nil
}

// GetOrCreateClientFromMachine extracts the KubevirtConfig reference from a Machine
// and returns the appropriate client.
//
// Logic:
// 1. If Machine has annotation "vitistack.io/kubevirt-config", use that config
// 2. If DEFAULT_KUBEVIRT_CONFIG env var is set, use that config
// 3. If exactly ONE KubevirtConfig exists, automatically use it
// 4. If MULTIPLE KubevirtConfigs exist without explicit reference:
//   - Search all clusters for the VirtualMachine (<machine-name>)
//   - If found, use that cluster and set annotation
//   - If not found in any cluster, return error (ambiguous placement)
//
// Returns: client, configName, needsAnnotationUpdate, error
// TODO: Once Machine CRD is updated, use machine.Spec.KubevirtConfigRef
func (m *KubevirtClientManager) GetOrCreateClientFromMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (client.Client, string, bool, error) {
	// TODO: Update this to use machine.Spec.KubevirtConfigRef once the field is added to the CRD
	// For now, check machine annotations for kubevirt-config
	kubevirtConfigName := ""
	needsAnnotationUpdate := false

	if machine.Annotations != nil {
		kubevirtConfigName = machine.Annotations["vitistack.io/kubevirt-config"]
	}

	if kubevirtConfigName == "" {
		// If no specific config is referenced, try to use a default one
		kubevirtConfigName = viper.GetString("DEFAULT_KUBEVIRT_CONFIG")
		if kubevirtConfigName == "" {
			// No default configured, check available KubevirtConfigs
			configs, err := m.ListKubevirtConfigs(ctx)
			if err != nil {
				return nil, "", false, fmt.Errorf("machine %s/%s has no KubevirtConfig reference and failed to list available configs: %w", machine.Namespace, machine.Name, err)
			}
			if len(configs) == 0 {
				return nil, "", false, fmt.Errorf("machine %s/%s has no KubevirtConfig reference and no KubevirtConfigs are available", machine.Namespace, machine.Name)
			}

			if len(configs) == 1 {
				// Only one config exists, use it automatically
				kubevirtConfigName = configs[0].Name
				needsAnnotationUpdate = true
			} else {
				// Multiple configs exist - search all clusters for the VM
				kubevirtConfigName = m.findVMInClusters(ctx, machine, configs)
				if kubevirtConfigName != "" {
					// Found VM in a cluster, set annotation
					needsAnnotationUpdate = true
				} else {
					// VM not found in any cluster - ambiguous placement
					return nil, "", false, fmt.Errorf("machine %s/%s has no KubevirtConfig reference and %d configs are available - cannot determine placement. Please set annotation 'vitistack.io/kubevirt-config' or DEFAULT_KUBEVIRT_CONFIG env var",
						machine.Namespace, machine.Name, len(configs))
				}
			}
		} else {
			// We're using the default config
			needsAnnotationUpdate = true
		}
	}

	remoteClient, err := m.GetClientForConfig(ctx, kubevirtConfigName)
	if err != nil {
		return nil, "", false, err
	}

	return remoteClient, kubevirtConfigName, needsAnnotationUpdate, nil
}

// findVMInClusters searches for a VirtualMachine across all available clusters
// Returns the config name where the VM was found, or empty string if not found
func (m *KubevirtClientManager) findVMInClusters(ctx context.Context, machine *vitistackv1alpha1.Machine, configs []vitistackv1alpha1.KubevirtConfig) string {
	vmName := machine.Name

	for i := range configs {
		config := &configs[i]
		remoteClient, err := m.GetClientForConfig(ctx, config.Name)
		if err != nil {
			// Skip this cluster if we can't connect
			continue
		}

		// Try to get the VM from this cluster
		vm := &kubevirtv1.VirtualMachine{}
		err = remoteClient.Get(ctx, client.ObjectKey{
			Namespace: machine.Namespace,
			Name:      vmName,
		}, vm)

		if err == nil {
			// Found the VM in this cluster!
			return config.Name
		}
		// Continue searching other clusters
	}

	// VM not found in any cluster
	return ""
}

// HealthCheck performs a health check on all cached clients
func (m *KubevirtClientManager) HealthCheck(ctx context.Context) map[string]error {
	m.mu.RLock()
	configNames := make([]string, 0, len(m.clients))
	for name := range m.clients {
		configNames = append(configNames, name)
	}
	m.mu.RUnlock()

	results := make(map[string]error)
	for _, name := range configNames {
		if err := m.ValidateConnection(ctx, name); err != nil {
			results[name] = err
		}
	}

	return results
}

// GetRESTConfigForConfig returns a REST config for the specified KubevirtConfig
// This is useful for creating informers or other clients that need direct REST access
func (m *KubevirtClientManager) GetRESTConfigForConfig(ctx context.Context, kubevirtConfigName string) (*rest.Config, error) {
	// Fetch the KubevirtConfig CRD (cluster-scoped, no namespace)
	kubevirtConfig := &vitistackv1alpha1.KubevirtConfig{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{
		Name: kubevirtConfigName,
	}, kubevirtConfig); err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("KubevirtConfig %s not found", kubevirtConfigName)
		}
		return nil, fmt.Errorf("failed to get KubevirtConfig: %w", err)
	}

	// Get the kubeconfig secret (secrets are namespaced)
	secretName := kubevirtConfig.Spec.KubeconfigSecretRef
	if secretName == "" {
		return nil, fmt.Errorf("KubevirtConfig %s has no kubeconfigSecretRef", kubevirtConfigName)
	}

	secretNamespace := kubevirtConfig.Spec.SecretNamespace
	if secretNamespace == "" {
		return nil, fmt.Errorf("KubevirtConfig %s has no secretNamespace", kubevirtConfigName)
	}

	secret := &corev1.Secret{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{
		Name:      secretName,
		Namespace: secretNamespace,
	}, secret); err != nil {
		return nil, fmt.Errorf("failed to get kubeconfig secret %s/%s: %w", secretNamespace, secretName, err)
	}

	// Extract kubeconfig from secret
	kubeconfigData, ok := secret.Data["kubeconfig"]
	if !ok {
		return nil, fmt.Errorf("secret %s/%s does not contain 'kubeconfig' key", secretNamespace, secretName)
	}

	// Create REST config from kubeconfig
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigData)
	if err != nil {
		return nil, fmt.Errorf("failed to create REST config from kubeconfig: %w", err)
	}

	return restConfig, nil
}
