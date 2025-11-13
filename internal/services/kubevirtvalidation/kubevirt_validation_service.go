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

package kubevirtvalidation

import (
	"context"
	"fmt"

	"github.com/vitistack/common/pkg/loggers/vlog"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/pkg/clients"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CheckKubeVirtOnRemoteClusters checks if KubeVirt components are properly running on each remote cluster
func CheckKubeVirtOnRemoteClusters(ctx context.Context, clientMgr clients.ClientManager, configs []vitistackv1alpha1.KubevirtConfig) []string {
	var allErrors []string

	for i := range configs {
		config := &configs[i]
		vlog.Info(fmt.Sprintf("Checking KubeVirt on cluster: %s", config.Name))

		// Get client for this cluster
		remoteClient, err := clientMgr.GetClientForConfig(ctx, config.Name)
		if err != nil {
			allErrors = append(allErrors, fmt.Sprintf("Cluster '%s': Failed to get client: %s", config.Name, err.Error()))
			continue
		}

		// Get REST config to create kubernetes clientset
		restConfig, err := clientMgr.GetRESTConfigForConfig(ctx, config.Name)
		if err != nil {
			allErrors = append(allErrors, fmt.Sprintf("Cluster '%s': Failed to get REST config: %s", config.Name, err.Error()))
			continue
		}

		// Create kubernetes clientset for this cluster
		kubernetesClient, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			allErrors = append(allErrors, fmt.Sprintf("Cluster '%s': Failed to create kubernetes client: %s", config.Name, err.Error()))
			continue
		}

		// Check KubeVirt components on this cluster
		clusterErrors := checkKubeVirtComponents(ctx, kubernetesClient, remoteClient, config.Name)
		allErrors = append(allErrors, clusterErrors...)
	}

	return allErrors
}

// checkKubeVirtComponents checks if KubeVirt components are running on a specific cluster
func checkKubeVirtComponents(ctx context.Context, kubernetesClient kubernetes.Interface, remoteClient client.Client, clusterName string) []string {
	errors := make([]string, 0, 5) // Pre-allocate for expected number of potential errors
	namespace := "kubevirt"        // KubeVirt is typically installed in the 'kubevirt' namespace

	// Define the expected components
	componentsToCheck := []string{
		"virt-api",
		"virt-controller",
		"virt-handler",
		"virt-operator",
	}

	// Check if namespace exists
	_, err := kubernetesClient.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		errors = append(errors, fmt.Sprintf("Cluster '%s': KubeVirt namespace '%s' not found: %s", clusterName, namespace, err.Error()))
		return errors
	}

	// Check deployments and daemonsets for each component
	for _, component := range componentsToCheck {
		// First try checking for a deployment
		deployment, err := kubernetesClient.AppsV1().Deployments(namespace).Get(ctx, component, metav1.GetOptions{})
		if err == nil {
			// Deployment exists, check if it's ready
			if deployment.Status.ReadyReplicas == 0 {
				errors = append(errors, fmt.Sprintf("Cluster '%s': Component '%s' is not ready (0 ready replicas)", clusterName, component))
			} else {
				vlog.Info(fmt.Sprintf("✅ Cluster '%s': Component '%s' is ready (%d/%d replicas)",
					clusterName, component, deployment.Status.ReadyReplicas, deployment.Status.Replicas))
			}
			continue
		}

		// If not a deployment, try checking for a daemonset (for virt-handler)
		daemonset, err := kubernetesClient.AppsV1().DaemonSets(namespace).Get(ctx, component, metav1.GetOptions{})
		if err == nil {
			// DaemonSet exists, check if it's ready
			if daemonset.Status.NumberReady == 0 {
				errors = append(errors, fmt.Sprintf("Cluster '%s': Component '%s' is not ready (0 ready nodes)", clusterName, component))
			} else {
				vlog.Info(fmt.Sprintf("✅ Cluster '%s': Component '%s' is ready (%d/%d nodes)",
					clusterName, component, daemonset.Status.NumberReady, daemonset.Status.DesiredNumberScheduled))
			}
			continue
		}

		// Component not found as a Deployment or DaemonSet
		errors = append(errors, fmt.Sprintf("Cluster '%s': Component '%s' not found", clusterName, component))
	}

	// Check if KubeVirt CR is in ready state using the dynamic client
	kubevirtReady, err := checkKubeVirtCROnCluster(ctx, remoteClient, namespace)
	switch {
	case err != nil:
		errors = append(errors, fmt.Sprintf("Cluster '%s': Failed to check KubeVirt CR status: %s", clusterName, err.Error()))
	case !kubevirtReady:
		errors = append(errors, fmt.Sprintf("Cluster '%s': KubeVirt CR is not in Ready state", clusterName))
	default:
		vlog.Info(fmt.Sprintf("✅ Cluster '%s': KubeVirt CR is in Ready state", clusterName))
	}

	return errors
}

// checkKubeVirtCROnCluster checks if the KubeVirt CR is in Ready state on a specific cluster
func checkKubeVirtCROnCluster(ctx context.Context, remoteClient client.Client, namespace string) (bool, error) {
	// List KubeVirt CRs
	kubevirtList := &unstructured.UnstructuredList{}
	kubevirtList.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "kubevirt.io",
		Version: "v1",
		Kind:    "KubeVirtList",
	})

	err := remoteClient.List(ctx, kubevirtList, client.InNamespace(namespace))
	if err != nil {
		return false, err
	}

	// Check for at least one KubeVirt CR in Ready state
	if len(kubevirtList.Items) == 0 {
		return false, fmt.Errorf("no KubeVirt CRs found in namespace %s", namespace)
	}

	for _, kubevirt := range kubevirtList.Items {
		conditions, found, err := unstructured.NestedSlice(kubevirt.Object, "status", "conditions")
		if err != nil || !found {
			continue
		}

		for _, condition := range conditions {
			conditionMap, ok := condition.(map[string]interface{})
			if !ok {
				continue
			}

			conditionType, typeFound := conditionMap["type"]
			status, statusFound := conditionMap["status"]

			if typeFound && statusFound && conditionType == "Available" && status == "True" {
				return true, nil
			}
		}
	}

	return false, nil
}
