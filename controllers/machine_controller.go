/*
Copyright 2025.

Licensed under the Apache License,func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	// Fetch the Machine instance
	machine := &vitistackv1alpha1.Machine{}ion 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

// MachineReconciler reconciles a Machine object
type MachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	MachineFinalizer = "machine.vitistack.io/finalizer"
)

// +kubebuilder:rbac:groups=vitistack.io,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vitistack.io,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vitistack.io,resources=machines/finalizers,verbs=update
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines/status,verbs=get
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances/status,verbs=get
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch

func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the Machine instance
	machine := &vitistackv1alpha1.Machine{}
	err := r.Get(ctx, req.NamespacedName, machine)
	if err != nil {
		if errors.IsNotFound(err) {
			// Machine resource not found. Ignoring since object must be deleted
			logger.Info("Machine resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request
		logger.Error(err, "Failed to get Machine")
		return ctrl.Result{}, err
	}

	// Check if the Machine is being deleted
	if machine.GetDeletionTimestamp() != nil {
		return r.handleDeletion(ctx, machine)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(machine, MachineFinalizer) {
		controllerutil.AddFinalizer(machine, MachineFinalizer)
		if err := r.Update(ctx, machine); err != nil {
			logger.Error(err, "Failed to add finalizer to Machine")
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Create or update the KubeVirt VirtualMachine
	vm := &kubevirtv1.VirtualMachine{}
	vmName := fmt.Sprintf("vm-%s", machine.Name)
	vmNamespacedName := types.NamespacedName{
		Name:      vmName,
		Namespace: machine.Namespace,
	}

	err = r.Get(ctx, vmNamespacedName, vm)
	if err != nil && errors.IsNotFound(err) {
		// VirtualMachine doesn't exist, create it
		vm, err = r.createVirtualMachine(ctx, machine)
		if err != nil {
			logger.Error(err, "Failed to create VirtualMachine")
			if statusErr := r.updateMachineStatus(ctx, machine, "Failed"); statusErr != nil {
				logger.Error(statusErr, "Failed to update machine status after VM creation failure")
			}
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
		logger.Info("Created VirtualMachine", "virtualmachine", vm.Name)
	} else if err != nil {
		logger.Error(err, "Failed to get VirtualMachine")
		return ctrl.Result{}, err
	}

	// Get the VirtualMachineInstance if it exists
	vmi := &kubevirtv1.VirtualMachineInstance{}
	vmiNamespacedName := types.NamespacedName{
		Name:      vmName,
		Namespace: machine.Namespace,
	}

	vmiExists := false
	err = r.Get(ctx, vmiNamespacedName, vmi)
	if err != nil && !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get VirtualMachineInstance")
		return ctrl.Result{}, err
	} else if err == nil {
		vmiExists = true
		logger.Info("Found VirtualMachineInstance", "vmi", vmi.Name, "phase", vmi.Status.Phase)
	}

	// Update Machine status based on VirtualMachine and VirtualMachineInstance status
	return r.updateMachineStatusFromVMAndVMI(ctx, machine, vm, vmi, vmiExists)
}

func (r *MachineReconciler) createPVC(ctx context.Context, machine *vitistackv1alpha1.Machine, pvcName string) (*corev1.PersistentVolumeClaim, error) {
	logger := log.FromContext(ctx)

	// Default storage size - use a reasonable default since there's no storage field in Machine spec
	storageSize := "10Gi"

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: machine.Namespace,
			Labels: map[string]string{
				"managed-by":     "kubevirt-operator",
				"source-machine": machine.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(storageSize),
				},
			},
		},
	}

	// Set Machine as the owner of the PVC
	if err := controllerutil.SetControllerReference(machine, pvc, r.Scheme); err != nil {
		return nil, err
	}

	if err := r.Create(ctx, pvc); err != nil {
		return nil, err
	}

	logger.Info("Successfully created PVC", "pvc", pvc.Name, "size", storageSize)
	return pvc, nil
}

func (r *MachineReconciler) createVirtualMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (*kubevirtv1.VirtualMachine, error) {
	logger := log.FromContext(ctx)

	vmName := fmt.Sprintf("vm-%s", machine.Name)
	pvcName := fmt.Sprintf("%s-pvc", vmName)

	// Create PVC first
	_, err := r.createPVC(ctx, machine, pvcName)
	if err != nil {
		logger.Error(err, "Failed to create PVC")
		return nil, err
	}

	// Default resource values based on machine spec
	memoryRequest := "1Gi"
	cpuRequest := "1"

	// Use simple defaults based on machine type from the external CRD
	if machine.Spec.InstanceType != "" {
		switch machine.Spec.InstanceType {
		case "small":
			memoryRequest = "1Gi"
			cpuRequest = "1"
		case "medium":
			memoryRequest = "2Gi"
			cpuRequest = "2"
		case "large":
			memoryRequest = "4Gi"
			cpuRequest = "4"
		default:
			memoryRequest = "1Gi"
			cpuRequest = "1"
		}
	}

	// Override with spec values if provided
	if machine.Spec.Memory > 0 {
		memoryRequest = fmt.Sprintf("%dMi", machine.Spec.Memory/1024/1024) // Convert bytes to MiB
	}
	if machine.Spec.CPU.Cores > 0 {
		cpuRequest = fmt.Sprintf("%d", machine.Spec.CPU.Cores)
	}

	// Create a local variable for RunStrategy since we need its address
	// Using RunStrategyAlways to ensure the VM starts automatically (replaces deprecated Running: true)
	runStrategy := kubevirtv1.RunStrategyAlways
	cpuModel := viper.GetString(consts.CPU_MODEL)

	vm := &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vmName,
			Namespace: machine.Namespace,
			Labels: map[string]string{
				"managed-by":     "kubevirt-operator",
				"source-machine": machine.Name,
			},
		},
		Spec: kubevirtv1.VirtualMachineSpec{
			RunStrategy: &runStrategy, // Use modern RunStrategy instead of deprecated Running field
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"managed-by":     "kubevirt-operator",
						"source-machine": machine.Name,
					},
				},
				Spec: kubevirtv1.VirtualMachineInstanceSpec{
					Domain: kubevirtv1.DomainSpec{
						Resources: kubevirtv1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceMemory: resource.MustParse(memoryRequest),
								corev1.ResourceCPU:    resource.MustParse(cpuRequest),
							},
						},
						CPU: &kubevirtv1.CPU{
							//Cores: uint32(coresRequest),
							Model: cpuModel,
						},
						Devices: kubevirtv1.Devices{
							Disks: []kubevirtv1.Disk{
								{
									Name: "root",
									DiskDevice: kubevirtv1.DiskDevice{
										Disk: &kubevirtv1.DiskTarget{
											Bus: "virtio",
										},
									},
								},
							},
						},
					},
					Volumes: []kubevirtv1.Volume{
						{
							Name: "root",
							VolumeSource: kubevirtv1.VolumeSource{
								PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{
									PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
										ClaimName: pvcName,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	// Set Machine as the owner of the VirtualMachine
	if err := controllerutil.SetControllerReference(machine, vm, r.Scheme); err != nil {
		return nil, err
	}

	if err := r.Create(ctx, vm); err != nil {
		return nil, err
	}

	logger.Info("Successfully created VirtualMachine", "virtualmachine", vm.Name)
	return vm, nil
}

func (r *MachineReconciler) updateMachineStatusFromVM(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Determine state based on VirtualMachine state
	var state string
	var requeue bool

	if vm.Status.Ready {
		state = "Running"
	} else if vm.Status.Created {
		state = "Pending"
		requeue = true
	} else {
		state = "Pending"
		requeue = true
	}

	machine.Status.Phase = string(vm.Status.PrintableStatus)
	machine.Status.State = string(vm.Status.PrintableStatus)
	machine.Status.Provider = "kubevirt"

	// todo fetch datacenter region and zone

	// Update machine status
	if err := r.updateMachineStatus(ctx, machine, state); err != nil {
		logger.Error(err, "Failed to update Machine status")
		return ctrl.Result{}, err
	}

	if requeue {
		// Requeue to check status again
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (r *MachineReconciler) updateMachineStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, state string) error {
	return r.updateMachineStatusWithDetails(ctx, machine, state, "")
}

func (r *MachineReconciler) updateMachineStatusWithDetails(ctx context.Context, machine *vitistackv1alpha1.Machine, state string, errorDetails string) error {
	logger := log.FromContext(ctx)

	// Retry logic for status updates to handle resource version conflicts
	maxRetries := 3
	for i := range maxRetries {
		// Get the latest version of the Machine to avoid resource version conflicts
		latestMachine := &vitistackv1alpha1.Machine{}
		if err := r.Get(ctx, types.NamespacedName{
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
		if err := r.Status().Update(ctx, latestMachine); err != nil {
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

func (r *MachineReconciler) handleDeletion(ctx context.Context, machine *vitistackv1alpha1.Machine) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(machine, MachineFinalizer) {
		// Delete associated VirtualMachine
		vmName := fmt.Sprintf("vm-%s", machine.Name)
		vm := &kubevirtv1.VirtualMachine{}
		vmNamespacedName := types.NamespacedName{
			Name:      vmName,
			Namespace: machine.Namespace,
		}

		err := r.Get(ctx, vmNamespacedName, vm)
		if err == nil {
			// VirtualMachine exists, delete it
			if err := r.Delete(ctx, vm); err != nil {
				logger.Error(err, "Failed to delete VirtualMachine")
				return ctrl.Result{}, err
			}
			logger.Info("Deleted VirtualMachine", "virtualmachine", vm.Name)
		} else if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get VirtualMachine for deletion")
			return ctrl.Result{}, err
		}

		// Delete associated PVC
		pvcName := fmt.Sprintf("%s-pvc", vmName)
		pvc := &corev1.PersistentVolumeClaim{}
		pvcNamespacedName := types.NamespacedName{
			Name:      pvcName,
			Namespace: machine.Namespace,
		}

		err = r.Get(ctx, pvcNamespacedName, pvc)
		if err == nil {
			// PVC exists, delete it
			if err := r.Delete(ctx, pvc); err != nil {
				logger.Error(err, "Failed to delete PVC")
				return ctrl.Result{}, err
			}
			logger.Info("Deleted PVC", "pvc", pvc.Name)
		} else if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get PVC for deletion")
			return ctrl.Result{}, err
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(machine, MachineFinalizer)
		if err := r.Update(ctx, machine); err != nil {
			logger.Error(err, "Failed to remove finalizer from Machine")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *MachineReconciler) updateMachineStatusFromVMAndVMI(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine, vmi *kubevirtv1.VirtualMachineInstance, vmiExists bool) (ctrl.Result, error) {
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
		if err := r.checkVMIErrorsAndUpdateStatus(ctx, machine, vmi); err != nil {
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
		if err := r.checkVMErrorsAndUpdateStatus(ctx, machine, vm); err != nil {
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
	if err := r.updateMachineStatus(ctx, machine, state); err != nil {
		logger.Error(err, "Failed to update Machine status")
		return ctrl.Result{}, err
	}

	if requeue {
		// Requeue to check status again
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// getVMIEvents fetches events related to a VirtualMachineInstance and returns error events
func (r *MachineReconciler) getVMIEvents(ctx context.Context, vmi *kubevirtv1.VirtualMachineInstance, machine *vitistackv1alpha1.Machine) ([]string, error) {
	logger := log.FromContext(ctx)

	// Get events for the VMI
	eventList := &corev1.EventList{}
	listOpts := &client.ListOptions{
		Namespace: vmi.Namespace,
	}

	if err := r.List(ctx, eventList, listOpts); err != nil {
		logger.Error(err, "Failed to list events for VMI", "vmi", vmi.Name)
		return nil, err
	}

	var errorEvents []string
	for _, event := range eventList.Items {
		// Filter events related to this VMI
		if event.InvolvedObject.Name != vmi.Name ||
			event.InvolvedObject.Kind != "VirtualMachineInstance" ||
			event.InvolvedObject.UID != vmi.UID {
			continue
		}

		// Only consider events that occurred after the machine was created
		// This helps avoid stale events from previous instances
		if event.FirstTimestamp.Before(&machine.CreationTimestamp) {
			continue
		}

		// Also filter by VMI creation time to be extra sure
		if !vmi.CreationTimestamp.IsZero() && event.FirstTimestamp.Before(&vmi.CreationTimestamp) {
			continue
		}

		// Check for error events (Warning type or specific error reasons)
		if event.Type == "Warning" ||
			strings.Contains(strings.ToLower(event.Reason), "error") ||
			strings.Contains(strings.ToLower(event.Reason), "failed") ||
			strings.Contains(strings.ToLower(event.Message), "error") ||
			strings.Contains(strings.ToLower(event.Message), "syncfailed") ||
			strings.Contains(strings.ToLower(event.Message), "failed") {

			// Format timestamp safely
			timestamp := "unknown"
			if !event.LastTimestamp.IsZero() {
				timestamp = event.LastTimestamp.Format("15:04:05")
			} else if !event.FirstTimestamp.IsZero() {
				timestamp = event.FirstTimestamp.Format("15:04:05")
			}

			errorMsg := fmt.Sprintf("[%s] %s: %s",
				timestamp,
				event.Reason,
				event.Message)
			errorEvents = append(errorEvents, errorMsg)

			logger.Info("Found VMI error event",
				"vmi", vmi.Name,
				"vmiUID", vmi.UID,
				"eventUID", event.InvolvedObject.UID,
				"reason", event.Reason,
				"message", event.Message,
				"type", event.Type,
				"eventTime", event.FirstTimestamp)
		}
	}

	return errorEvents, nil
}

// checkVMIErrorsAndUpdateStatus checks for VMI errors and updates machine status accordingly
func (r *MachineReconciler) checkVMIErrorsAndUpdateStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, vmi *kubevirtv1.VirtualMachineInstance) error {
	logger := log.FromContext(ctx)

	// Get VMI events to check for errors
	errorEvents, err := r.getVMIEvents(ctx, vmi, machine)
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

// getVMEvents fetches events related to a VirtualMachine and returns error events
func (r *MachineReconciler) getVMEvents(ctx context.Context, vm *kubevirtv1.VirtualMachine, machine *vitistackv1alpha1.Machine) ([]string, error) {
	logger := log.FromContext(ctx)

	// Get events for the VM
	eventList := &corev1.EventList{}
	listOpts := &client.ListOptions{
		Namespace: vm.Namespace,
	}

	if err := r.List(ctx, eventList, listOpts); err != nil {
		logger.Error(err, "Failed to list events for VM", "vm", vm.Name)
		return nil, err
	}

	var errorEvents []string
	for _, event := range eventList.Items {
		// Filter events related to this VM
		if event.InvolvedObject.Name != vm.Name ||
			event.InvolvedObject.Kind != "VirtualMachine" ||
			event.InvolvedObject.UID != vm.UID {
			continue
		}

		// Only consider events that occurred after the machine was created
		// This helps avoid stale events from previous instances
		if event.FirstTimestamp.Before(&machine.CreationTimestamp) {
			continue
		}

		// Also filter by VM creation time to be extra sure
		if !vm.CreationTimestamp.IsZero() && event.FirstTimestamp.Before(&vm.CreationTimestamp) {
			continue
		}

		// Check for error events (Warning type or specific error reasons)
		if event.Type == "Warning" ||
			strings.Contains(strings.ToLower(event.Reason), "error") ||
			strings.Contains(strings.ToLower(event.Reason), "failed") ||
			strings.Contains(strings.ToLower(event.Message), "error") ||
			strings.Contains(strings.ToLower(event.Message), "failed") {

			// Format timestamp safely
			timestamp := "unknown"
			if !event.LastTimestamp.IsZero() {
				timestamp = event.LastTimestamp.Format("15:04:05")
			} else if !event.FirstTimestamp.IsZero() {
				timestamp = event.FirstTimestamp.Format("15:04:05")
			}

			errorMsg := fmt.Sprintf("[%s] %s: %s",
				timestamp,
				event.Reason,
				event.Message)
			errorEvents = append(errorEvents, errorMsg)

			logger.Info("Found VM error event",
				"vm", vm.Name,
				"vmUID", vm.UID,
				"eventUID", event.InvolvedObject.UID,
				"reason", event.Reason,
				"message", event.Message,
				"type", event.Type,
				"eventTime", event.FirstTimestamp)
		}
	}

	return errorEvents, nil
}

// checkVMErrorsAndUpdateStatus checks for VM errors and updates machine status accordingly
func (r *MachineReconciler) checkVMErrorsAndUpdateStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) error {
	logger := log.FromContext(ctx)

	// Get VM events to check for errors
	errorEvents, err := r.getVMEvents(ctx, vm, machine)
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

// SetupWithManager sets up the controller with the Manager.
func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vitistackv1alpha1.Machine{}).
		Owns(&kubevirtv1.VirtualMachine{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Watches(&kubevirtv1.VirtualMachineInstance{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &vitistackv1alpha1.Machine{})).
		Complete(r)
}
