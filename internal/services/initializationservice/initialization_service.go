package initializationservice

import (
	"context"
	"fmt"
	"strings"

	"github.com/vitistack/kubevirt-operator/pkg/clients"

	"github.com/NorskHelsenett/ror/pkg/rlog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func CheckPrerequisites() {
	rlog.Info("Running prerequisite checks...")
	CheckCRDs()
	CheckRunningKubeVirtVM()
	rlog.Info("✅ Prerequisite checks passed")
}

func CheckCRDs() {
	var errors []string
	kubernetesclient := clients.Kubernetes

	// Check if the cluster is accessible by listing namespaces
	namespaces, err := kubernetesclient.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		errors = append(errors, fmt.Sprintf("Failed to access cluster: %s", err.Error()))
	} else if len(namespaces.Items) == 0 {
		errors = append(errors, "No namespaces found in the cluster")
	}

	// Only continue with CRD checks if we can access the cluster
	if len(errors) == 0 {
		// Check CRDs for kubevirt.io/v1
		errors = checkGroupVersionCRDs(errors, "kubevirt.io/v1", []string{
			"KubeVirt",
			"VirtualMachineInstanceMigration",
			"VirtualMachineInstancePreset",
			"VirtualMachineInstanceReplicaSet",
			"VirtualMachineInstance",
			"VirtualMachine",
		})

		// Check CRDs for kubevirt.io/v1alpha3
		errors = checkGroupVersionCRDs(errors, "pool.kubevirt.io/v1alpha1", []string{
			"VirtualMachinePool",
		})
	}

	// If we collected any errors, report them all together
	if len(errors) > 0 {
		errorMessage := fmt.Sprintf("Prerequisite checks failed:\n- %s", strings.Join(errors, "\n- "))
		rlog.Error(errorMessage, nil)
		panic(errorMessage)
	}

	rlog.Info("✅ All prerequisite checks passed")
}

// checkGroupVersionCRDs checks for the existence of specific CRDs in a given API group version
func checkGroupVersionCRDs(errors []string, groupVersion string, requiredCRDs []string) []string {
	kubernetesclient := clients.Kubernetes
	resources, err := kubernetesclient.Discovery().ServerResourcesForGroupVersion(groupVersion)

	if err != nil {
		errors = append(errors, fmt.Sprintf("CRDs are not installed properly, could not find the resource group version installed: %s, error: %s", groupVersion, err.Error()))
		return errors
	}

	if len(resources.APIResources) == 0 {
		errors = append(errors, fmt.Sprintf("No resources found for group version %s", groupVersion))
		return errors
	}

	// Check for each required CRD
	for _, crdKind := range requiredCRDs {
		if !crdExists(resources, crdKind) {
			errors = append(errors, fmt.Sprintf("%s CRD is not installed (group version: %s)", crdKind, groupVersion))
		} else {
			rlog.Info(fmt.Sprintf("✅ %s CRD is installed (group version: %s)", crdKind, groupVersion))
		}
	}

	return errors
}

// crdExists verifies that a specific CRD exists in the API resources
func crdExists(resources *metav1.APIResourceList, crdKind string) bool {
	for i := range resources.APIResources {
		if resources.APIResources[i].Kind == crdKind {
			return true
		}
	}
	return false
}

// CheckRunningKubeVirtVM checks if KubeVirt components are properly running in the cluster
func CheckRunningKubeVirtVM() {
	var errors []string
	kubernetesclient := clients.Kubernetes

	// Define the expected components and their namespaces
	componentsToCheck := map[string][]string{
		"kubevirt": {
			"virt-api",
			"virt-controller",
			"virt-handler",
			"virt-operator",
		},
	}

	// Check each component
	for namespace, components := range componentsToCheck {
		// Check if namespace exists
		_, err := kubernetesclient.CoreV1().Namespaces().Get(context.TODO(), namespace, metav1.GetOptions{})
		if err != nil {
			errors = append(errors, fmt.Sprintf("KubeVirt namespace '%s' not found: %s", namespace, err.Error()))
			continue
		}

		// Check deployments for each component
		for _, component := range components {
			// First try checking for a deployment
			deployment, err := kubernetesclient.AppsV1().Deployments(namespace).Get(context.TODO(), component, metav1.GetOptions{})
			if err == nil {
				// Deployment exists, check if it's ready
				if deployment.Status.ReadyReplicas == 0 {
					errors = append(errors, fmt.Sprintf("KubeVirt component '%s' in namespace '%s' is not ready (0 ready replicas)", component, namespace))
				} else {
					rlog.Info(fmt.Sprintf("✅ KubeVirt component '%s' in namespace '%s' is ready (%d/%d replicas)",
						component, namespace, deployment.Status.ReadyReplicas, deployment.Status.Replicas))
				}
				continue
			}

			// If not a deployment, try checking for a daemonset (for virt-handler)
			daemonset, err := kubernetesclient.AppsV1().DaemonSets(namespace).Get(context.TODO(), component, metav1.GetOptions{})
			if err == nil {
				// DaemonSet exists, check if it's ready
				if daemonset.Status.NumberReady == 0 {
					errors = append(errors, fmt.Sprintf("KubeVirt component '%s' in namespace '%s' is not ready (0 ready nodes)", component, namespace))
				} else {
					rlog.Info(fmt.Sprintf("✅ KubeVirt component '%s' in namespace '%s' is ready (%d/%d nodes)",
						component, namespace, daemonset.Status.NumberReady, daemonset.Status.DesiredNumberScheduled))
				}
				continue
			}

			// Component not found as a Deployment or DaemonSet
			errors = append(errors, fmt.Sprintf("KubeVirt component '%s' in namespace '%s' not found", component, namespace))
		}

		// Check if KubeVirt CR is in ready state
		kubevirtCR, err := checkKubeVirtCR(namespace)
		switch {
		case err != nil:
			errors = append(errors, fmt.Sprintf("Failed to check KubeVirt CR status: %s", err.Error()))
		case !kubevirtCR:
			errors = append(errors, "KubeVirt CR is not in Ready state")
		default:
			rlog.Info("✅ KubeVirt CR is in Ready state")
		}
	}

	// If we collected any errors, report them all together
	if len(errors) > 0 {
		errorMessage := fmt.Sprintf("KubeVirt component checks failed:\n- %s", strings.Join(errors, "\n- "))
		rlog.Error(errorMessage, nil)
		panic(errorMessage)
	}

	rlog.Info("✅ All KubeVirt components are running properly")
}

// checkKubeVirtCR checks if the KubeVirt CR is in Ready state using dynamic client
func checkKubeVirtCR(namespace string) (bool, error) {
	dynamicClient := clients.DynamicClient

	// Define the GVR for KubeVirt CR
	gvr := schema.GroupVersionResource{
		Group:    "kubevirt.io",
		Version:  "v1",
		Resource: "kubevirts",
	}

	// Get all KubeVirt CRs in the namespace
	kubevirts, err := dynamicClient.Resource(gvr).Namespace(namespace).List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return false, err
	}

	// Check for at least one KubeVirt CR in Ready state
	if len(kubevirts.Items) == 0 {
		return false, fmt.Errorf("no KubeVirt CRs found in namespace %s", namespace)
	}

	for _, kubevirt := range kubevirts.Items {
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
