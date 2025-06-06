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
			r.updateMachineStatus(ctx, machine, "Failed", fmt.Sprintf("Failed to create VirtualMachine: %v", err))
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

	// Default resource values if not specified in machine spec
	memoryRequest := "1Gi"
	cpuRequest := "1"

	// Convert machine spec to VM spec if available
	if machine.Status.Memory > 0 {
		memoryRequest = fmt.Sprintf("%dBi", machine.Status.Memory)
	}
	if machine.Status.CPUs > 0 {
		cpuRequest = fmt.Sprintf("%d", machine.Status.CPUs)
	}

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
			Running: &[]bool{true}[0], // Start the VM
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

	// Determine status based on VirtualMachine state
	var status, state string
	var requeue bool

	if vm.Status.Ready {
		status = "Ready"
		state = "Running"
	} else if vm.Status.Created {
		status = "Creating"
		state = "Pending"
		requeue = true
	} else {
		status = "Pending"
		state = "Pending"
		requeue = true
	}

	// Update machine status
	if err := r.updateMachineStatus(ctx, machine, status, state); err != nil {
		logger.Error(err, "Failed to update Machine status")
		return ctrl.Result{}, err
	}

	if requeue {
		// Requeue to check status again
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	return ctrl.Result{}, nil
}

func (r *MachineReconciler) updateMachineStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, status, state string) error {
	machine.Status.Status = status
	machine.Status.State = state
	machine.Status.LastUpdated = metav1.Now()
	machine.Status.Provider = "kubevirt"

	return r.Status().Update(ctx, machine)
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
