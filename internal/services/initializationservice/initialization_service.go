package initializationservice

import (
	"context"
	"fmt"
	"strings"

	"github.com/vitistack/common/pkg/loggers/vlog"
	"github.com/vitistack/common/pkg/operator/crdcheck"
	"github.com/vitistack/kubevirt-operator/internal/services/crdvalidation"
	"github.com/vitistack/kubevirt-operator/internal/services/kubevirtvalidation"
	"github.com/vitistack/kubevirt-operator/pkg/clients"
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
