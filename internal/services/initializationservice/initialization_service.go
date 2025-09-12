package initializationservice

import (
	"context"
	"fmt"
	"strings"

	"github.com/vitistack/common/pkg/loggers/vlog"
	"github.com/vitistack/common/pkg/operator/crdcheck"
	"github.com/vitistack/kubevirt-operator/pkg/clients"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func CheckPrerequisites() {
	vlog.Info("Running prerequisite checks...")

	crdcheck.MustEnsureInstalled(context.TODO(),
		// your CRD plural
		crdcheck.Ref{Group: "vitistack.io", Version: "v1alpha1", Resource: "machines"},
		crdcheck.Ref{Group: "kubevirt.io", Version: "v1", Resource: "kubevirts"},
		crdcheck.Ref{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstancemigrations"},
		crdcheck.Ref{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstancepresets"},
		crdcheck.Ref{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstancereplicasets"},
		crdcheck.Ref{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachineinstances"},
		crdcheck.Ref{Group: "kubevirt.io", Version: "v1", Resource: "virtualmachines"},
		crdcheck.Ref{Group: "pool.kubevirt.io", Version: "v1alpha1", Resource: "virtualmachinepools"},
	)

	CheckRunningKubeVirtVM()
	vlog.Info("✅ Prerequisite checks passed")
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
					vlog.Info(fmt.Sprintf("✅ KubeVirt component '%s' in namespace '%s' is ready (%d/%d replicas)",
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
					vlog.Info(fmt.Sprintf("✅ KubeVirt component '%s' in namespace '%s' is ready (%d/%d nodes)",
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
			vlog.Info("✅ KubeVirt CR is in Ready state")
		}
	}

	// If we collected any errors, report them all together
	if len(errors) > 0 {
		errorMessage := fmt.Sprintf("KubeVirt component checks failed:\n- %s", strings.Join(errors, "\n- "))
		vlog.Error(errorMessage, nil)
		panic(errorMessage)
	}

	vlog.Info("✅ All KubeVirt components are running properly")
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
