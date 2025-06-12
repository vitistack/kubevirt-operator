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
	"sigs.k8s.io/controller-runtime/pkg/log"

	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
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

	// Update Machine status based on VirtualMachine status
	return r.updateMachineStatusFromVM(ctx, machine, vm)
}

func (r *MachineReconciler) createVirtualMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (*kubevirtv1.VirtualMachine, error) {
	logger := log.FromContext(ctx)

	vmName := fmt.Sprintf("vm-%s", machine.Name)

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
								ContainerDisk: &kubevirtv1.ContainerDiskSource{
									Image: "quay.io/kubevirt/cirros-container-disk-demo",
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
	logger := log.FromContext(ctx)

	// Retry logic for status updates to handle resource version conflicts
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
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
		latestMachine.Status.State = state
		latestMachine.Status.Phase = state
		latestMachine.Status.LastUpdated = metav1.Now()
		latestMachine.Status.Provider = "kubevirt"

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

		// Remove finalizer
		controllerutil.RemoveFinalizer(machine, MachineFinalizer)
		if err := r.Update(ctx, machine); err != nil {
			logger.Error(err, "Failed to remove finalizer from Machine")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vitistackv1alpha1.Machine{}).
		Owns(&kubevirtv1.VirtualMachine{}).
		Complete(r)
}
