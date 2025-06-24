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

// Package controllers provides the main reconciler for Machine resources.
// This package has been refactored to use dedicated manager packages in internal/machine/:
// - storage: Handles PVC creation, deletion, and storage class management
// - vm: Handles KubeVirt VirtualMachine creation and disk/volume management
// - status: Handles Machine status updates and error reporting
// - events: Handles VM/VMI event collection and error detection

package controllers

import (
	"context"
	"fmt"
	"time"

	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/machine/events"
	"github.com/vitistack/kubevirt-operator/internal/machine/status"
	"github.com/vitistack/kubevirt-operator/internal/machine/storage"
	"github.com/vitistack/kubevirt-operator/internal/machine/vm"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// MachineReconciler reconciles a Machine object
type MachineReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Manager instances for different concerns
	StorageManager *storage.StorageManager
	VMManager      *vm.VMManager
	StatusManager  *status.StatusManager
	EventsManager  *events.EventsManager
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
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

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
	virtualmachine := &kubevirtv1.VirtualMachine{}
	vmName := fmt.Sprintf("vm-%s", machine.Name)
	vmNamespacedName := types.NamespacedName{
		Name:      vmName,
		Namespace: machine.Namespace,
	}

	err = r.Get(ctx, vmNamespacedName, virtualmachine)
	if err != nil && errors.IsNotFound(err) {
		// VirtualMachine doesn't exist, create it
		// First create PVCs using the storage manager
		pvcNames, err := r.StorageManager.CreatePVCsFromDiskSpecs(ctx, machine, vmName)
		if err != nil {
			logger.Error(err, "Failed to create PVCs")
			if statusErr := r.StatusManager.UpdateMachineStatus(ctx, machine, "Failed"); statusErr != nil {
				logger.Error(statusErr, "Failed to update machine status after PVC creation failure")
			}
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}

		// Then create the VM using the VM manager
		virtualmachine, err = r.VMManager.CreateVirtualMachine(ctx, machine, vmName, pvcNames)
		if err != nil {
			logger.Error(err, "Failed to create VirtualMachine")
			if statusErr := r.StatusManager.UpdateMachineStatus(ctx, machine, "Failed"); statusErr != nil {
				logger.Error(statusErr, "Failed to update machine status after VM creation failure")
			}
			return ctrl.Result{RequeueAfter: time.Minute}, err
		}
		logger.Info("Created VirtualMachine", "virtualmachine", virtualmachine.Name)
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
	return r.StatusManager.UpdateMachineStatusFromVMAndVMI(ctx, machine, virtualmachine, vmi, vmiExists)
}

func (r *MachineReconciler) handleDeletion(ctx context.Context, machine *vitistackv1alpha1.Machine) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(machine, MachineFinalizer) {
		// Delete associated VirtualMachine
		vmName := fmt.Sprintf("vm-%s", machine.Name)
		virtualMachine := &kubevirtv1.VirtualMachine{}
		vmNamespacedName := types.NamespacedName{
			Name:      vmName,
			Namespace: machine.Namespace,
		}

		err := r.Get(ctx, vmNamespacedName, virtualMachine)
		if err == nil {
			// VirtualMachine exists, delete it
			if err := r.Delete(ctx, virtualMachine); err != nil {
				logger.Error(err, "Failed to delete VirtualMachine")
				return ctrl.Result{}, err
			}
			logger.Info("Deleted VirtualMachine", "virtualmachine", virtualMachine.Name)
		} else if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get VirtualMachine for deletion")
			return ctrl.Result{}, err
		}

		// Delete all associated PVCs
		if err := r.StorageManager.DeleteAssociatedPVCs(ctx, machine, vmName); err != nil {
			logger.Error(err, "Failed to delete associated PVCs")
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

// NewMachineReconciler creates a new MachineReconciler with initialized managers
func NewMachineReconciler(c client.Client, scheme *runtime.Scheme) *MachineReconciler {
	eventsManager := events.NewManager(c)
	statusManager := status.NewManager(c, eventsManager)

	return &MachineReconciler{
		Client:         c,
		Scheme:         scheme,
		StorageManager: storage.NewManager(c, scheme),
		VMManager:      vm.NewManager(c, scheme),
		StatusManager:  statusManager,
		EventsManager:  eventsManager,
	}
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
