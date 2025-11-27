package initializationservice

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/viper"
	"github.com/vitistack/common/pkg/loggers/vlog"
	"github.com/vitistack/common/pkg/operator/crdcheck"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	"github.com/vitistack/kubevirt-operator/internal/services"
	"github.com/vitistack/kubevirt-operator/internal/services/crdvalidation"
	"github.com/vitistack/kubevirt-operator/internal/services/kubevirtvalidation"
	"github.com/vitistack/kubevirt-operator/internal/services/vitistackservice"
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

	// List all KubevirtConfigs (cluster-scoped)
	configs, err := clientMgr.ListKubevirtConfigs(ctx)
	if err != nil {
		vlog.Error("Failed to list KubevirtConfigs", err)
		panic(fmt.Sprintf("Failed to list KubevirtConfigs: %s", err.Error()))
	}

	if len(configs) == 0 {
		errorMessage := "No KubevirtConfig resources found. Please create at least one KubevirtConfig."
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

// RegisterMachineProviderCRDIfNeeded checks if the MachineProvider for this operator exists
// If not found, it looks up the matching KubevirtConfig and registers the MachineProvider
func RegisterMachineProviderCRDIfNeeded(namespace string) error {
	ctx := context.TODO()

	// Get the machine provider name for this operator
	machineProviderName := viper.GetString(consts.NAME_MACHINE_PROVIDER)
	if machineProviderName == "" {
		return fmt.Errorf("NAME_MACHINE_PROVIDER not configured")
	}

	vlog.Info("Checking for MachineProvider", "name", machineProviderName)

	// First check if vitistack object has a matching MachineProvider
	vitistackName := viper.GetString(consts.VITISTACK_NAME)
	if vitistackName != "" {
		vitistack, err := vitistackservice.FetchVitistackByName(ctx, vitistackName)
		if err == nil && vitistack != nil {
			for _, mpRef := range vitistack.Spec.MachineProviders {
				if mpRef.Name == machineProviderName {
					vlog.Info("MachineProvider found in Vitistack spec", "name", machineProviderName, "vitistack", vitistackName)
					return nil
				}
			}
		}
	}

	// Check if the MachineProvider already exists
	existingProvider, err := services.MachineProviderService.FetchMachineProviderByName(ctx, machineProviderName)
	if err == nil && existingProvider != nil {
		vlog.Info("MachineProvider already exists", "name", machineProviderName)
		return nil
	}

	vlog.Info("MachineProvider not found, looking for matching KubevirtConfig", "name", machineProviderName)

	// Find the matching KubevirtConfig by name
	kubevirtConfig, err := services.KubevirtConfigService.FetchKubevirtConfigByName(ctx, machineProviderName, namespace)
	if err != nil {
		return fmt.Errorf("failed to find KubevirtConfig with name %s: %w", machineProviderName, err)
	}

	vlog.Info("Found matching KubevirtConfig, registering MachineProvider", "name", kubevirtConfig.Name)

	// Register the MachineProvider
	err = registerMachineProvider(ctx, kubevirtConfig)
	if err != nil {
		return fmt.Errorf("failed to register MachineProvider CRD: %w", err)
	}

	vlog.Info("MachineProvider CRD registered successfully", "name", machineProviderName)
	return nil
}

func registerMachineProvider(ctx context.Context, kubeconfig *vitistackv1alpha1.KubevirtConfig) error {
	// fetch vitistack object
	defaultRegion := "unknown"
	vitistackName := viper.GetString(consts.VITISTACK_NAME)
	if vitistackName == "" {
		vlog.Info("VITISTACK_NAME not configured, using default region", "region", defaultRegion)
		return fmt.Errorf("VITISTACK_NAME not configured")
	}

	vitistack, err := vitistackservice.FetchVitistackByName(ctx, vitistackName)
	if err != nil {
		vlog.Warn("Failed to fetch Vitistack, using default region",
			"vitistackName", vitistackName,
			"error", err,
			"region", defaultRegion)
		return err
	}

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
			Region:       vitistack.Spec.Region,
			Authentication: vitistackv1alpha1.ProviderAuthentication{
				Type: "credentials",
				CredentialsRef: &vitistackv1alpha1.CredentialsReference{
					SecretName: kubeconfig.Spec.KubeconfigSecretRef,
					Namespace:  kubeconfig.Spec.SecretNamespace,
				},
			},
		},
	}

	return services.MachineProviderService.CreateMachineProvider(ctx, machineProvider)
}
