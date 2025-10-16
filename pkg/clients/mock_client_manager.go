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

	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// MockClientManager is a mock implementation of ClientManager for testing
type MockClientManager struct {
	// MockedClients maps config names to clients
	MockedClients map[string]client.Client

	// MockedConfigs stores KubevirtConfig objects
	MockedConfigs []vitistackv1alpha1.KubevirtConfig

	// ConfigNamespace is the namespace where configs are stored
	ConfigNamespace string

	// GetClientForConfigFunc allows custom behavior for GetClientForConfig
	GetClientForConfigFunc func(ctx context.Context, kubevirtConfigName string) (client.Client, error)

	// GetOrCreateClientFromMachineFunc allows custom behavior
	GetOrCreateClientFromMachineFunc func(ctx context.Context, machine *vitistackv1alpha1.Machine) (client.Client, string, bool, error)

	// Errors to return for specific operations
	GetClientError          error
	ListConfigsError        error
	ValidateConnectionError error

	// Call tracking
	GetClientCalls              []string
	InvalidateClientCalls       []string
	ValidateConnectionCalls     []string
	GetOrCreateFromMachineCalls int
}

// Ensure MockClientManager implements ClientManager interface
var _ ClientManager = (*MockClientManager)(nil)

// NewMockClientManager creates a new mock client manager for testing
func NewMockClientManager() *MockClientManager {
	return &MockClientManager{
		MockedClients:   make(map[string]client.Client),
		MockedConfigs:   []vitistackv1alpha1.KubevirtConfig{},
		ConfigNamespace: "default",
	}
}

// GetClientForConfig returns a mocked client for the specified config
func (m *MockClientManager) GetClientForConfig(ctx context.Context, kubevirtConfigName string) (client.Client, error) {
	m.GetClientCalls = append(m.GetClientCalls, kubevirtConfigName)

	if m.GetClientForConfigFunc != nil {
		return m.GetClientForConfigFunc(ctx, kubevirtConfigName)
	}

	if m.GetClientError != nil {
		return nil, m.GetClientError
	}

	if mockedClient, exists := m.MockedClients[kubevirtConfigName]; exists {
		return mockedClient, nil
	}

	return nil, fmt.Errorf("mock: KubevirtConfig %s not found", kubevirtConfigName)
}

// ListKubevirtConfigs returns the mocked configs
func (m *MockClientManager) ListKubevirtConfigs(ctx context.Context) ([]vitistackv1alpha1.KubevirtConfig, error) {
	if m.ListConfigsError != nil {
		return nil, m.ListConfigsError
	}

	return m.MockedConfigs, nil
}

// InvalidateClient tracks invalidation calls
func (m *MockClientManager) InvalidateClient(kubevirtConfigName string) {
	m.InvalidateClientCalls = append(m.InvalidateClientCalls, kubevirtConfigName)
	delete(m.MockedClients, kubevirtConfigName)
}

// InvalidateAll clears all mocked clients
func (m *MockClientManager) InvalidateAll() {
	m.MockedClients = make(map[string]client.Client)
}

// GetConfigNamespace returns the mocked namespace
func (m *MockClientManager) GetConfigNamespace() string {
	return m.ConfigNamespace
}

// ValidateConnection tracks validation calls
func (m *MockClientManager) ValidateConnection(ctx context.Context, kubevirtConfigName string) error {
	m.ValidateConnectionCalls = append(m.ValidateConnectionCalls, kubevirtConfigName)

	if m.ValidateConnectionError != nil {
		return m.ValidateConnectionError
	}

	if _, exists := m.MockedClients[kubevirtConfigName]; !exists {
		return fmt.Errorf("mock: client not found for config %s", kubevirtConfigName)
	}

	return nil
}

// GetOrCreateClientFromMachine returns a mocked client based on machine annotations
func (m *MockClientManager) GetOrCreateClientFromMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (client.Client, string, bool, error) {
	m.GetOrCreateFromMachineCalls++

	if m.GetOrCreateClientFromMachineFunc != nil {
		return m.GetOrCreateClientFromMachineFunc(ctx, machine)
	}

	// Default behavior: look for annotation
	kubevirtConfigName := ""
	needsUpdate := false

	if machine.Annotations != nil {
		kubevirtConfigName = machine.Annotations["vitistack.io/kubevirt-config"]
	}

	if kubevirtConfigName == "" {
		// Simulate the real logic:
		// - If exactly one config exists, use it automatically
		// - If multiple configs exist, fail (would need to search clusters)
		switch {
		case len(m.MockedConfigs) == 1:
			kubevirtConfigName = m.MockedConfigs[0].Name
			needsUpdate = true
		case len(m.MockedConfigs) == 0 && len(m.MockedClients) == 1:
			// Fallback: if no configs but exactly one client
			for name := range m.MockedClients {
				kubevirtConfigName = name
				needsUpdate = true
				break
			}
		case len(m.MockedConfigs) > 1 || len(m.MockedClients) > 1:
			// Multiple configs - would require searching all clusters in real implementation
			return nil, "", false, fmt.Errorf("mock: machine has no kubevirt-config annotation and %d configs are available - cannot determine placement", len(m.MockedConfigs))
		}
	}

	if kubevirtConfigName == "" {
		return nil, "", false, fmt.Errorf("mock: machine has no kubevirt-config annotation and no configs available")
	}

	remoteClient, err := m.GetClientForConfig(ctx, kubevirtConfigName)
	if err != nil {
		return nil, "", false, err
	}

	return remoteClient, kubevirtConfigName, needsUpdate, nil
}

// HealthCheck returns mocked health status
func (m *MockClientManager) HealthCheck(ctx context.Context) map[string]error {
	results := make(map[string]error)

	for configName := range m.MockedClients {
		if err := m.ValidateConnection(ctx, configName); err != nil {
			results[configName] = err
		}
	}

	return results
}

// GetRESTConfigForConfig returns a mocked REST config (nil for now as it's hard to mock)
func (m *MockClientManager) GetRESTConfigForConfig(ctx context.Context, kubevirtConfigName string) (*rest.Config, error) {
	if m.GetClientError != nil {
		return nil, m.GetClientError
	}

	if _, exists := m.MockedClients[kubevirtConfigName]; !exists {
		return nil, fmt.Errorf("mock: config not found")
	}

	// Return a basic REST config for testing
	return &rest.Config{
		Host: fmt.Sprintf("https://mock-%s.example.com:6443", kubevirtConfigName),
	}, nil
}

// AddMockedClient adds a client to the mock manager
func (m *MockClientManager) AddMockedClient(configName string, kubevirtClient client.Client) {
	m.MockedClients[configName] = kubevirtClient
}

// AddMockedConfig adds a KubevirtConfig to the mock manager
func (m *MockClientManager) AddMockedConfig(config *vitistackv1alpha1.KubevirtConfig) {
	m.MockedConfigs = append(m.MockedConfigs, *config)
}

// ResetCallTracking resets all call tracking counters
func (m *MockClientManager) ResetCallTracking() {
	m.GetClientCalls = []string{}
	m.InvalidateClientCalls = []string{}
	m.ValidateConnectionCalls = []string{}
	m.GetOrCreateFromMachineCalls = 0
}
