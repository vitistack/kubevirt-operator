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

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ClientManager defines the interface for managing multiple Kubernetes clients
// for different KubeVirt clusters
type ClientManager interface {
	// GetClientForConfig returns a Kubernetes client for the specified KubevirtConfig
	// The client is cached and reused for subsequent calls
	GetClientForConfig(ctx context.Context, kubevirtConfigName string) (client.Client, error)

	// ListKubevirtConfigs returns all KubevirtConfig CRDs in the configured namespace
	ListKubevirtConfigs(ctx context.Context) ([]vitistackv1alpha1.KubevirtConfig, error)

	// InvalidateClient removes a cached client, forcing it to be recreated on next access
	InvalidateClient(kubevirtConfigName string)

	// InvalidateAll removes all cached clients
	InvalidateAll()

	// GetConfigNamespace returns the namespace where KubevirtConfig CRDs are stored
	GetConfigNamespace() string

	// ValidateConnection tests the connection to a KubeVirt cluster
	ValidateConnection(ctx context.Context, kubevirtConfigName string) error

	// GetOrCreateClientFromMachine gets or creates a client based on Machine's KubevirtConfig reference.
	// Returns the client, the name of the KubevirtConfig used, and whether the annotation needs updating.
	GetOrCreateClientFromMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (client.Client, string, bool, error) // HealthCheck performs a health check on all cached clients
	// Returns a map of config names to errors (empty map if all healthy)
	HealthCheck(ctx context.Context) map[string]error

	// GetRESTConfigForConfig returns a REST config for the specified KubevirtConfig
	// This is useful for creating informers or other clients that need direct REST access
	GetRESTConfigForConfig(ctx context.Context, kubevirtConfigName string) (*rest.Config, error)

	// GetSupervisorClient returns the Kubernetes client for the supervisor cluster
	GetSupervisorClient() client.Client
}
