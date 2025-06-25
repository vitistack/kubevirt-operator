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

package status

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/machine/events"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// StatusManager handles machine status updates and monitoring
type StatusManager struct {
	client.Client
	EventsManager *events.EventsManager
}

// NewManager creates a new status manager
func NewManager(c client.Client, eventsManager *events.EventsManager) *StatusManager {
	return &StatusManager{
		Client:        c,
		EventsManager: eventsManager,
	}
}

// UpdateMachineStatus updates the machine status with the given state
func (m *StatusManager) UpdateMachineStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, state string) error {
	return m.UpdateMachineStatusWithDetails(ctx, machine, state, "")
}

// UpdateMachineStatusWithDetails updates the machine status with detailed error information
func (m *StatusManager) UpdateMachineStatusWithDetails(ctx context.Context, machine *vitistackv1alpha1.Machine, state string, errorDetails string) error {
	logger := log.FromContext(ctx)

	// Retry logic for status updates to handle resource version conflicts
	maxRetries := 3
	for i := range maxRetries {
		// Get the latest version of the Machine to avoid resource version conflicts
		latestMachine := &vitistackv1alpha1.Machine{}
		if err := m.Get(ctx, types.NamespacedName{
			Name:      machine.Name,
			Namespace: machine.Namespace,
		}, latestMachine); err != nil {
			logger.Error(err, "Failed to get latest Machine for status update")
			return err
		}

		// Update the status fields - only use fields that exist in the external CRD
		latestMachine.Status = machine.Status
		latestMachine.Status.LastUpdated = metav1.Now()
		latestMachine.Status.Provider = "kubevirt"

		// Note: The Status field and Conditions field might not exist in the external CRD Go types
		// We log error details instead and rely on the State field for error indication

		// Attempt to update the status
		if err := m.Status().Update(ctx, latestMachine); err != nil {
			if errors.IsConflict(err) && i < maxRetries-1 {
				logger.Info("Resource conflict during status update, retrying", "attempt", i+1)
				// Brief delay before retry
				time.Sleep(time.Millisecond * 100)
				continue
			}
			return err
		}

		// Success - update the passed machine object with the latest values for consistency
		machine.Status = latestMachine.Status
		machine.ObjectMeta.ResourceVersion = latestMachine.ObjectMeta.ResourceVersion
		return nil
	}

	return fmt.Errorf("failed to update Machine status after %d retries", maxRetries)
}

// UpdateMachineStatusFromVMAndVMI determines and updates machine status based on VM and VMI state
func (m *StatusManager) UpdateMachineStatusFromVMAndVMI(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine, vmi *kubevirtv1.VirtualMachineInstance, vmiExists bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Determine state based on VirtualMachine and VirtualMachineInstance state
	var state string
	var requeue bool
	var phase string

	if vmiExists {
		// Use VMI status as it's more accurate for running instances
		switch vmi.Status.Phase {
		case kubevirtv1.Running:
			state = "Running"
			phase = "Running"

			// When VMI is running, try to get Pod information for hostIP and podIP
			if err := m.updatePodInformation(ctx, machine, vmi); err != nil {
				logger.Error(err, "Failed to update pod information", "vmi", vmi.Name)
				// Don't fail the reconciliation for this, just log the error
			}

		case kubevirtv1.Pending:
			state = "Pending"
			phase = "Pending"
			requeue = true
		case kubevirtv1.Scheduling:
			state = "Scheduling"
			phase = "Scheduling"
			requeue = true
		case kubevirtv1.Scheduled:
			state = "Scheduled"
			phase = "Scheduled"
			requeue = true
		case kubevirtv1.Succeeded:
			state = "Succeeded"
			phase = "Succeeded"
		case kubevirtv1.Failed:
			state = "Failed"
			phase = "Failed"
		default:
			state = "Unknown"
			phase = "Unknown"
			requeue = true
		}

		logger.Info("VMI status", "phase", vmi.Status.Phase, "state", state)

		// Check for VMI errors that might prevent startup
		if err := m.checkVMIErrorsAndUpdateStatus(ctx, machine, vmi); err != nil {
			logger.Error(err, "Failed to check VMI errors")
			// Continue processing even if error checking fails
		}

		// Additional VMI status information
		if len(vmi.Status.Conditions) > 0 {
			for _, condition := range vmi.Status.Conditions {
				logger.Info("VMI condition", "type", condition.Type, "status", condition.Status, "reason", condition.Reason)
			}
		}

	} else {
		// Fall back to VM status if VMI doesn't exist yet
		if vm.Status.Ready {
			state = "Running"
			phase = "Running"
		} else if vm.Status.Created {
			state = "Pending"
			phase = "Pending"
			requeue = true
		} else {
			state = "Pending"
			phase = "Pending"
			requeue = true
		}

		logger.Info("VM status (no VMI)", "ready", vm.Status.Ready, "created", vm.Status.Created, "state", state)

		// Check for VM errors that might prevent VMI creation
		if err := m.checkVMErrorsAndUpdateStatus(ctx, machine, vm); err != nil {
			logger.Error(err, "Failed to check VM errors")
			// Continue processing even if error checking fails
		}
	}

	// Update machine status fields
	machine.Status.Phase = phase
	machine.Status.State = state
	machine.Status.Provider = "kubevirt"

	// todo fetch datacenter region and zone

	// Update machine status
	if err := m.UpdateMachineStatus(ctx, machine, state); err != nil {
		logger.Error(err, "Failed to update Machine status")
		return ctrl.Result{}, err
	}

	if requeue {
		// Requeue to check status again
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// checkVMIErrorsAndUpdateStatus checks for VMI errors and updates machine status accordingly
func (m *StatusManager) checkVMIErrorsAndUpdateStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, vmi *kubevirtv1.VirtualMachineInstance) error {
	logger := log.FromContext(ctx)

	// Get VMI events to check for errors
	errorEvents, err := m.EventsManager.GetVMIEvents(ctx, vmi, machine)
	if err != nil {
		logger.Error(err, "Failed to get VMI events")
		return err
	}

	// If there are error events and VMI is in a problematic state, update status
	if len(errorEvents) > 0 && (vmi.Status.Phase == kubevirtv1.Pending ||
		vmi.Status.Phase == kubevirtv1.Failed ||
		vmi.Status.Phase == kubevirtv1.Scheduling ||
		vmi.Status.Phase == kubevirtv1.Scheduled) {

		machine.Status.State = "Failed"
		machine.Status.Phase = "Failed"

		// Update the status field with error information
		errorSummary := fmt.Sprintf("VM startup failed - %d error(s) found: %s",
			len(errorEvents),
			strings.Join(errorEvents, "; "))

		// Truncate if too long to avoid status field size limits
		if len(errorSummary) > 500 {
			errorSummary = errorSummary[:497] + "..."
		}

		// Store the error details in status fields that are available in the CRD
		machine.Status.State = "Failed"
		machine.Status.Phase = "Failed"

		logger.Error(nil, "VMI has error events preventing startup",
			"vmi", vmi.Name,
			"errorCount", len(errorEvents),
			"phase", vmi.Status.Phase,
			"errorDetails", errorSummary)

		// Use the proper failure fields that exist in the CRD schema
		machine.Status.FailureMessage = &errorSummary
		reason := "VMIError"
		machine.Status.FailureReason = &reason
	}

	return nil
}

// checkVMErrorsAndUpdateStatus checks for VM errors and updates machine status accordingly
func (m *StatusManager) checkVMErrorsAndUpdateStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) error {
	logger := log.FromContext(ctx)

	// Get VM events to check for errors
	errorEvents, err := m.EventsManager.GetVMEvents(ctx, vm, machine)
	if err != nil {
		logger.Error(err, "Failed to get VM events")
		return err
	}

	// If there are error events and VM is not ready, update status
	if len(errorEvents) > 0 && !vm.Status.Ready {
		machine.Status.State = "Failed"
		machine.Status.Phase = "Failed"

		// Update the status field with error information
		errorSummary := fmt.Sprintf("VM creation failed - %d error(s) found: %s",
			len(errorEvents),
			strings.Join(errorEvents, "; "))

		// Truncate if too long to avoid status field size limits
		if len(errorSummary) > 500 {
			errorSummary = errorSummary[:497] + "..."
		}

		logger.Error(nil, "VM has error events preventing startup",
			"vm", vm.Name,
			"errorCount", len(errorEvents),
			"ready", vm.Status.Ready,
			"errorDetails", errorSummary)

		// Use the proper failure fields that exist in the CRD schema
		machine.Status.FailureMessage = &errorSummary
		reason := "VMError"
		machine.Status.FailureReason = &reason
	}

	return nil
}

// updatePodInformation fetches the Pod created by the VMI and extracts HostIP and PodIP
func (m *StatusManager) updatePodInformation(ctx context.Context, machine *vitistackv1alpha1.Machine, vmi *kubevirtv1.VirtualMachineInstance) error {
	logger := log.FromContext(ctx)

	// Get all Pods in the VMI's namespace and find one that contains the VMI name
	// KubeVirt creates Pods with names that contain the VMI name but may have additional suffixes
	podList := &corev1.PodList{}
	if err := m.List(ctx, podList, client.InNamespace(vmi.Namespace)); err != nil {
		return fmt.Errorf("failed to list Pods in namespace %s: %w", vmi.Namespace, err)
	}

	var matchedPod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if strings.Contains(pod.Name, vmi.Name) {
			// Additional check to ensure this is a KubeVirt Pod by checking labels
			if pod.Labels != nil {
				if kubevirtDomain, exists := pod.Labels["kubevirt.io/domain"]; exists && kubevirtDomain == vmi.Name {
					matchedPod = pod
					break
				}
				// Fallback: check if it's a virt-launcher pod
				if podType, exists := pod.Labels["kubevirt.io"]; exists && strings.Contains(podType, "virt-launcher") {
					matchedPod = pod
					break
				}
			}
		}
	}

	if matchedPod == nil {
		logger.Info("No Pod found for VMI", "vmi", vmi.Name, "namespace", vmi.Namespace)
		return nil // Not an error - Pod might not be created yet
	}

	logger.Info("Found Pod for VMI", "pod", matchedPod.Name, "namespace", matchedPod.Namespace, "hostIP", matchedPod.Status.HostIP, "podIP", matchedPod.Status.PodIP)

	// Extract HostIP and PodIP from the Pod
	hostIP := matchedPod.Status.HostIP
	podIP := matchedPod.Status.PodIP

	// Update Machine status with Pod information
	// We'll use existing IP address fields in the CRD for this information
	// Since the CRD has ipAddresses array, we'll use that for podIP
	// For hostIP, we can use a custom field or repurpose an existing one

	addPodIPToMachineStatus(podIP, machine, logger)

	// For hostIP, we can store it in the hostname field or create a custom way to track it
	// Let's use the hostname field to store host information
	if hostIP != "" {
		addPodIPToMachineStatus(hostIP, machine, logger)
	}

	return nil
}

func addPodIPToMachineStatus(podIP string, machine *vitistackv1alpha1.Machine, logger logr.Logger) {
	if podIP != "" {
		// Add podIP to the ipAddresses if not already present
		found := slices.Contains(machine.Status.IPAddresses, podIP)
		if !found {
			if machine.Status.IPAddresses == nil {
				machine.Status.IPAddresses = []string{}
			}
			machine.Status.IPAddresses = append(machine.Status.IPAddresses, podIP)
			logger.Info("Added Pod IP to Machine status", "podIP", podIP)
		}
	}
}
