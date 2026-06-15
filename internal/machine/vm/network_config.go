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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// generateShortNetworkConfigName generates a shortened name for NetworkConfiguration spec.name field
// to comply with the 32-byte limit. Follows patterns:
// - <cluster>-wrk<number> for workers (e.g., simple-cluster-worker-default-worker-pool-0 -> simple-cluster-wrk0)
// - <cluster>-ctp<number> for control planes (e.g., simple-cluster-control-plane-0 -> simple-cluster-ctp0)
func generateShortNetworkConfigName(machineName string) string {
	// Pattern 1: <cluster>-worker-default-worker-pool-<number> or <cluster>-worker-<pool>-pool-<number>
	// Extract cluster name and number, convert to <cluster>-wp<number>
	workerPattern := regexp.MustCompile(`^(.+?)-worker(?:-default)?(?:-worker)?-pool-(\d+)$`)
	if matches := workerPattern.FindStringSubmatch(machineName); matches != nil {
		clusterName := matches[1]
		poolNumber := matches[2]
		shortName := fmt.Sprintf("%s-wrk%s", clusterName, poolNumber)
		if len(shortName) <= 32 {
			return shortName
		}
	}

	// Pattern 2: <cluster>-control-plane-<number>
	// Convert to <cluster>-cp<number>
	controlPlanePattern := regexp.MustCompile(`^(.+?)-control-plane-(\d+)$`)
	if matches := controlPlanePattern.FindStringSubmatch(machineName); matches != nil {
		clusterName := matches[1]
		number := matches[2]
		shortName := fmt.Sprintf("%s-ctp%s", clusterName, number)
		if len(shortName) <= 32 {
			return shortName
		}
	}

	// Fallback: If patterns don't match or result is still too long,
	shortName := machineName
	if len(shortName) <= 32 {
		return shortName
	}

	// Truncate to 32 bytes
	return "" + machineName[:32]
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

		// Resolve the IP-allocation provider from the referenced NetworkNamespace
		// so the new NC declares which operator owns it (kea / static-ip-operator
		// / etc.). Without this, downstream allocator operators must fall back
		// to a deprecated default and emit a per-NC warning on every reconcile.
		// Missing NN or missing ipAllocation leaves Provider empty — same as the
		// pre-fix behavior, so legacy NNs continue to work.
		provider, err := m.resolveNCProvider(ctx, machine)
		if err != nil {
			return err
		}

		netConfig := &vitistackv1alpha1.NetworkConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      machine.Name,
				Namespace: machine.Namespace,
				Labels: map[string]string{
					vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
					LabelSourceMachine:                    machine.Name,
				},
			},
			Spec: vitistackv1alpha1.NetworkConfigurationSpec{
				NetworkNamespaceName: machine.Spec.Network.NetworkNamespaceName,
				Name:                 shortName,
				Description:          "Network configuration for machine " + machine.Name,
				Provider:             provider,
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

	// NetworkConfiguration exists — update only if the interface or its MAC changed.
	// static-ip-operator writes .status on the same object, so a no-op Update
	// here would race and lose (resourceVersion conflict).
	changed := false
	interfaceExists := false
	for i := range existingNetConfig.Spec.NetworkInterfaces {
		iface := &existingNetConfig.Spec.NetworkInterfaces[i]
		if iface.Name == networkName {
			interfaceExists = true
			if iface.MacAddress != macAddress {
				iface.MacAddress = macAddress
				changed = true
			}
			break
		}
	}
	if !interfaceExists {
		existingNetConfig.Spec.NetworkInterfaces = append(existingNetConfig.Spec.NetworkInterfaces, networkInterface)
		changed = true
	}

	// Backfill spec.provider on legacy NCs created before we wrote it.
	providerChanged, err := m.backfillNCProvider(ctx, machine, existingNetConfig)
	if err != nil {
		return err
	}
	if providerChanged {
		changed = true
	}

	if !changed {
		return nil
	}

	if err := m.supervisorClient.Update(ctx, existingNetConfig); err != nil {
		return fmt.Errorf("failed to update NetworkConfiguration: %w", err)
	}

	logger.Info("Updated NetworkConfiguration", "name", existingNetConfig.Name, "macAddress", macAddress)
	return nil
}

// backfillNCProvider sets spec.provider on an existing NC when it's unset.
// Each machine reconcile is a migration opportunity: if the referenced NN
// declares a provider and the NC still has it empty, copy it over. We only
// write when the NC carries our managed-by label so we never silently modify
// NCs owned by another controller. Returns whether the spec was mutated.
func (m *VMManager) backfillNCProvider(
	ctx context.Context,
	machine *vitistackv1alpha1.Machine,
	nc *vitistackv1alpha1.NetworkConfiguration,
) (bool, error) {
	if nc.Spec.Provider != "" || !hasManagedByLabel(nc) {
		return false, nil
	}
	provider, err := m.resolveNCProvider(ctx, machine)
	if err != nil {
		return false, err
	}
	if provider == "" {
		return false, nil
	}
	nc.Spec.Provider = provider
	log.FromContext(ctx).Info("Backfilling spec.provider on legacy NetworkConfiguration",
		"name", nc.Name, "provider", provider)
	return true, nil
}

// hasManagedByLabel reports whether the NC carries this operator's managed-by
// label. We use it to gate spec edits to NCs we created, so we never silently
// modify NCs owned by another controller.
func hasManagedByLabel(nc *vitistackv1alpha1.NetworkConfiguration) bool {
	if nc == nil {
		return false
	}
	expected := viper.GetString(consts.MANAGED_BY)
	if expected == "" {
		return false
	}
	return nc.GetLabels()[vitistackv1alpha1.ManagedByAnnotation] == expected
}

// EnsureNCProviderBackfilled is a self-healing pass that backfills spec.provider
// on the Machine's NetworkConfiguration without waiting for VM creation. It is
// safe to call on every Machine reconcile: when the NC is missing, foreign-owned,
// or already has a provider, it's a no-op (no API write). Only triggers an
// Update when there's a real change to commit. Logs and swallows errors so a
// transient failure doesn't fail the reconcile — the next reconcile will retry.
func (m *VMManager) EnsureNCProviderBackfilled(ctx context.Context, machine *vitistackv1alpha1.Machine) {
	logger := log.FromContext(ctx)
	nc := &vitistackv1alpha1.NetworkConfiguration{}
	if err := m.supervisorClient.Get(ctx, client.ObjectKey{Name: machine.Name, Namespace: machine.Namespace}, nc); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return
		}
		logger.V(1).Info("provider backfill: get NetworkConfiguration failed (will retry)",
			"machine", machine.Name, "namespace", machine.Namespace, "error", err.Error())
		return
	}
	changed, err := m.backfillNCProvider(ctx, machine, nc)
	if err != nil {
		logger.V(1).Info("provider backfill: resolve provider failed (will retry)",
			"machine", machine.Name, "namespace", machine.Namespace, "error", err.Error())
		return
	}
	if !changed {
		return
	}
	if err := m.supervisorClient.Update(ctx, nc); err != nil {
		logger.V(1).Info("provider backfill: update NetworkConfiguration failed (will retry)",
			"machine", machine.Name, "namespace", machine.Namespace, "error", err.Error())
	}
}

// resolveNCProvider returns the IP-allocation provider declared on the
// referenced NetworkNamespace, or "" if the NN can't be fetched or has no
// ipAllocation block. A NotFound NN is treated as "no provider yet" (a NN
// that hasn't been migrated, or is being created in parallel) so we still
// create the NC; downstream operators will warn until the NN is populated.
// Hard errors (anything other than NotFound) are propagated so a transient
// API failure doesn't silently produce a wrong-provider NC.
func (m *VMManager) resolveNCProvider(ctx context.Context, machine *vitistackv1alpha1.Machine) (string, error) {
	nnName := machine.Spec.Network.NetworkNamespaceName
	if nnName == "" {
		return "", nil
	}
	nn := &vitistackv1alpha1.NetworkNamespace{}
	if err := m.supervisorClient.Get(ctx, client.ObjectKey{Name: nnName, Namespace: machine.Namespace}, nn); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return "", fmt.Errorf("get NetworkNamespace %q for provider resolution: %w", nnName, err)
		}
		return "", nil
	}
	if nn.Spec.IPAllocation == nil {
		return "", nil
	}
	return nn.Spec.IPAllocation.Provider, nil
}

// resolveOrGenerateMAC returns the MAC already persisted on the Machine's
// NetworkConfiguration for this interface, generating a new one only when
// none exists. This keeps the MAC stable across reconciles so static-ip-operator
// allocates against a single MAC and the downstream VM spec matches what
// NetworkConfiguration.spec declares.
func (m *VMManager) resolveOrGenerateMAC(ctx context.Context, machine *vitistackv1alpha1.Machine, networkName string) (string, error) {
	existing := &vitistackv1alpha1.NetworkConfiguration{}
	err := m.supervisorClient.Get(ctx, client.ObjectKey{Name: machine.Name, Namespace: machine.Namespace}, existing)
	if err == nil {
		for i := range existing.Spec.NetworkInterfaces {
			iface := &existing.Spec.NetworkInterfaces[i]
			if iface.Name == networkName && iface.MacAddress != "" {
				return iface.MacAddress, nil
			}
		}
	} else if client.IgnoreNotFound(err) != nil {
		return "", fmt.Errorf("get NetworkConfiguration to reuse MAC: %w", err)
	}
	return m.MacGenerator.GetMACAddress()
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
	if managedBy, ok := labels[vitistackv1alpha1.ManagedByAnnotation]; !ok || managedBy != expectedManagedBy {
		logger.Info("NetworkConfiguration has unexpected managed-by label, skipping deletion",
			"name", machine.Name,
			vitistackv1alpha1.ManagedByAnnotation, managedBy)
		return fmt.Errorf("NetworkConfiguration managed-by label mismatch: expected '%s', got '%s'", expectedManagedBy, managedBy)
	}

	if sourceMachine, ok := labels[LabelSourceMachine]; !ok || sourceMachine != machine.Name {
		logger.Info("NetworkConfiguration has unexpected source-machine label, skipping deletion",
			"name", machine.Name,
			LabelSourceMachine, sourceMachine,
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
