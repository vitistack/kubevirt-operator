package initializationservice

import (
	"context"
	"fmt"
	"strings"

	"github.com/vitistack/common/pkg/loggers/vlog"
	"github.com/vitistack/common/pkg/operator/crdcheck"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/services"
	"github.com/vitistack/kubevirt-operator/internal/services/crdvalidation"
	"github.com/vitistack/kubevirt-operator/internal/services/kubevirtvalidation"
	"github.com/vitistack/kubevirt-operator/pkg/clients"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func CheckPrerequisites() {
	vlog.Info("Running prerequisite checks...")

	checks := []crdcheck.Ref{
		{Group: "vitistack.io", Version: "v1alpha1", Resource: "machines"},
		{Group: "vitistack.io", Version: "v1alpha1", Resource: "kubevirtconfigs"},
		{Group: "vitistack.io", Version: "v1alpha1", Resource: "networkconfigurations"},
	}

	crdcheck.MustEnsureInstalled(context.TODO(), checks...)
	vlog.Info("✅ Prerequisite checks passed (supervisor cluster)")
}

// CheckKubevirtClustersAvailable checks if KubevirtConfigs exist and KubeVirt is running on remote clusters
// This should be called after the ClientManager is available
func CheckKubevirtClustersAvailable(clientMgr clients.ClientManager) {
	vlog.Info("Checking KubeVirt clusters availability...")

	ctx := context.TODO()

	// Get KubevirtConfig namespace
	configNamespace := clientMgr.GetConfigNamespace()

	// List all KubevirtConfigs
	configs, err := clientMgr.ListKubevirtConfigs(ctx)
	if err != nil {
		vlog.Error("Failed to list KubevirtConfigs", err)
		panic(fmt.Sprintf("Failed to list KubevirtConfigs: %s", err.Error()))
	}

	if len(configs) == 0 {
		errorMessage := fmt.Sprintf("No KubevirtConfig resources found in namespace '%s'. Please create at least one KubevirtConfig.", configNamespace)
		vlog.Error(errorMessage, nil)
		panic(errorMessage)
	}

	configNames := make([]string, 0, len(configs))
	for i := range configs {
		configNames = append(configNames, configs[i].Name)
	}
	vlog.Info(fmt.Sprintf("✅ Found %d KubevirtConfig(s): %s", len(configs), strings.Join(configNames, ", ")))

	// Check KubeVirt on each remote cluster using the kubevirtvalidation service
	allErrors := kubevirtvalidation.CheckKubeVirtOnRemoteClusters(ctx, clientMgr, configs)
	if len(allErrors) > 0 {
		errorMessage := fmt.Sprintf("KubeVirt cluster checks failed:\n- %s", strings.Join(allErrors, "\n- "))
		vlog.Error(errorMessage, nil)
		panic(errorMessage)
	}

	// Check CRD compatibility using the crdvalidation service
	crdvalidation.CheckCRDCompatibility(ctx, clientMgr, configs)

	vlog.Info("✅ All KubeVirt clusters are available and ready")
}

// register machine provider crd if no objects exist
func RegisterMachineProviderCRDIfNeeded(namespace string) error {
	ctx := context.TODO()

	// list all MachineProvider objects
	kubeconfigs, err := services.KubevirtConfigService.FetchAllKubevirtConfigs(ctx, namespace)
	if err != nil {
		return fmt.Errorf("failed to list KubevirtConfig objects: %w", err)
	}
	vlog.Debug("Kubeconfigs", kubeconfigs)

	machineProviders, err := services.MachineProviderService.FetchAllMachineProviders(ctx)
	if err != nil {
		return fmt.Errorf("failed to list MachineProvider objects: %w", err)
	}

	vlog.Debug("MachineProviders", machineProviders)

	for i := range kubeconfigs {
		kubeconfig := kubeconfigs[i]
		found := false
		for j := range machineProviders {
			machineProvider := machineProviders[j]
			// MachineProvider is cluster-scoped (no namespace), so only compare by name
			if machineProvider.Name == kubeconfig.Name {
				found = true
				break
			}
		}
		if !found {
			vlog.Info("No MachineProvider found for KubevirtConfig " + kubeconfig.Name + ", registering CRD")
			err := registerMachineProvider(ctx, &kubeconfig)
			if err != nil {
				return fmt.Errorf("failed to register MachineProvider CRD: %w", err)
			}
			vlog.Info("MachineProvider CRD registered successfully")
		}
	}
	return nil
}

func registerMachineProvider(ctx context.Context, kubeconfig *vitistackv1alpha1.KubevirtConfig) error {
	// Create a MachineProvider object for the KubevirtConfig
	machineProvider := &vitistackv1alpha1.MachineProvider{
		ObjectMeta: metav1.ObjectMeta{
			Name: kubeconfig.Name,
			Labels: map[string]string{
				"vitistack.io/kubevirt-config": kubeconfig.Name,
			},
		},
		Spec: vitistackv1alpha1.MachineProviderSpec{
			DisplayName:  kubeconfig.Name,
			ProviderType: string(vitistackv1alpha1.MachineProviderTypeKubevirt),
			Region:       "default",
			Authentication: vitistackv1alpha1.ProviderAuthentication{
				Type: "credentials",
				CredentialsRef: &vitistackv1alpha1.CredentialsReference{
					SecretName: kubeconfig.Spec.KubeconfigSecretRef,
					Namespace:  kubeconfig.Namespace,
				},
			},
		},
	}

	return services.MachineProviderService.CreateMachineProvider(ctx, machineProvider)
}
