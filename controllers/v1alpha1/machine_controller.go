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

package v1alpha1

import (
	"context"
	"fmt"
	"strings"
	"time"

	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/machine/events"
	"github.com/vitistack/kubevirt-operator/internal/machine/network"
	"github.com/vitistack/kubevirt-operator/internal/machine/status"
	"github.com/vitistack/kubevirt-operator/internal/machine/storage"
	"github.com/vitistack/kubevirt-operator/internal/machine/vm"
	"github.com/vitistack/kubevirt-operator/pkg/macaddress"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	NetworkManager *network.NetworkManager
	MacGenerator   macaddress.MacAddressGenerator
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
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch

func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	machine, result, stop, err := r.fetchAndInitMachine(ctx, req)
	if stop || err != nil {
		return result, err
	}

	virtualmachine, vmName, result, stop, err := r.ensureVirtualMachine(ctx, machine)
	if stop || err != nil {
		return result, err
	}

	vmi, vmiExists, err := r.getVMI(ctx, vmName, machine.Namespace)
	if err != nil {
		logger.Error(err, "Failed to get VirtualMachineInstance")
		return ctrl.Result{}, err
	}

	return r.StatusManager.UpdateMachineStatusFromVMAndVMI(ctx, machine, virtualmachine, vmi, vmiExists)
}

// fetchAndInitMachine retrieves the Machine and handles deletion/finalizer logic.
// Returns: machine, result, stopReconcile, error
func (r *MachineReconciler) fetchAndInitMachine(ctx context.Context, req ctrl.Request) (*vitistackv1alpha1.Machine, ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)
	machine := &vitistackv1alpha1.Machine{}
	if err := r.Get(ctx, req.NamespacedName, machine); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("Machine resource not found")
			return nil, ctrl.Result{}, true, nil
		}
		logger.Error(err, "Failed to get Machine")
		return nil, ctrl.Result{}, true, err
	}

	// Guard: Only reconcile Machines for the kubevirt provider (default if unspecified)
	if !r.isKubevirtProvider(machine) {
		// If we somehow have our finalizer, ensure we don't block deletion
		if machine.GetDeletionTimestamp() != nil && controllerutil.ContainsFinalizer(machine, MachineFinalizer) {
			controllerutil.RemoveFinalizer(machine, MachineFinalizer)
			if err := r.Update(ctx, machine); err != nil {
				logger.Error(err, "Failed to remove finalizer from non-kubevirt Machine")
				return machine, ctrl.Result{}, true, err
			}
		}
		logger.V(1).Info("Skipping reconcile for non-kubevirt provider", "provider", r.getProviderName(machine))
		return machine, ctrl.Result{}, true, nil
	}

	if machine.GetDeletionTimestamp() != nil {
		if err := r.handleDeletion(ctx, machine); err != nil {
			return machine, ctrl.Result{}, true, err
		}
		return machine, ctrl.Result{}, true, nil
	}

	if !controllerutil.ContainsFinalizer(machine, MachineFinalizer) {
		controllerutil.AddFinalizer(machine, MachineFinalizer)
		if err := r.Update(ctx, machine); err != nil {
			logger.Error(err, "Failed to add finalizer to Machine")
			return machine, ctrl.Result{}, true, err
		}
		return machine, ctrl.Result{Requeue: true}, true, nil
	}
	return machine, ctrl.Result{}, false, nil
}

// ensureVirtualMachine fetches or creates the backing VirtualMachine.
// Returns: vm, vmName, result, stopReconcile, error
func (r *MachineReconciler) ensureVirtualMachine(ctx context.Context, machine *vitistackv1alpha1.Machine) (*kubevirtv1.VirtualMachine, string, ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)
	virtualmachine := &kubevirtv1.VirtualMachine{}
	vmName := fmt.Sprintf("vm-%s", machine.Name)
	vmKey := types.NamespacedName{Name: vmName, Namespace: machine.Namespace}

	if err := r.Get(ctx, vmKey, virtualmachine); err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get VirtualMachine")
			return nil, vmName, ctrl.Result{}, true, err
		}
		// Need to create VM
		networkConfiguration, netErr := r.NetworkManager.GetNetworkConfiguration(ctx, machine)
		if netErr != nil {
			r.recordNetworkFailure(ctx, machine, netErr)
			return nil, vmName, ctrl.Result{}, true, netErr
		}
		pvcNames, pvcErr := r.StorageManager.CreatePVCsFromDiskSpecs(ctx, machine, vmName)
		if pvcErr != nil {
			logger.Error(pvcErr, "Failed to create PVCs")
			_ = r.StatusManager.UpdateMachineStatus(ctx, machine, "Failed")
			return nil, vmName, ctrl.Result{RequeueAfter: time.Minute}, true, pvcErr
		}
		newVM, vmErr := r.VMManager.CreateVirtualMachine(ctx, machine, vmName, pvcNames, networkConfiguration)
		if vmErr != nil {
			logger.Error(vmErr, "Failed to create VirtualMachine")
			_ = r.StatusManager.UpdateMachineStatus(ctx, machine, "Failed")
			return nil, vmName, ctrl.Result{RequeueAfter: time.Minute}, true, vmErr
		}
		logger.Info("Created VirtualMachine", "virtualmachine", newVM.Name)
		return newVM, vmName, ctrl.Result{}, false, nil
	}
	return virtualmachine, vmName, ctrl.Result{}, false, nil
}

// recordNetworkFailure updates machine status for network configuration errors.
func (r *MachineReconciler) recordNetworkFailure(ctx context.Context, machine *vitistackv1alpha1.Machine, err error) {
	logger := log.FromContext(ctx)
	reason := "Failed to get network configuration in current namespace"
	errorTitle := "NetworkConfigurationError"
	logger.Error(err, reason)
	if machine.Status.Phase != vitistackv1alpha1.MachinePhaseFailed {
		machine.Status.Conditions = append(machine.Status.Conditions, vitistackv1alpha1.MachineCondition{
			Type:    vitistackv1alpha1.ConditionUnknown,
			Status:  string(metav1.ConditionFalse),
			Reason:  reason,
			Message: errorTitle,
		})
	}
	machine.Status.Phase = vitistackv1alpha1.MachinePhaseFailed
	machine.Status.LastUpdated = metav1.Now()
	machine.Status.FailureMessage = &errorTitle
	machine.Status.FailureReason = &reason
	if statusErr := r.StatusManager.UpdateMachineStatus(ctx, machine, errorTitle); statusErr != nil {
		logger.Error(statusErr, errorTitle)
	}
}

// getVMI retrieves the VMI if it exists.
func (r *MachineReconciler) getVMI(ctx context.Context, vmName, namespace string) (*kubevirtv1.VirtualMachineInstance, bool, error) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	key := types.NamespacedName{Name: vmName, Namespace: namespace}
	if err := r.Get(ctx, key, vmi); err != nil {
		if errors.IsNotFound(err) {
			return vmi, false, nil
		}
		return nil, false, err
	}
	return vmi, true, nil
}

func (r *MachineReconciler) handleDeletion(ctx context.Context, machine *vitistackv1alpha1.Machine) error {
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
				return err
			}
			logger.Info("Deleted VirtualMachine", "virtualmachine", virtualMachine.Name)
		} else if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get VirtualMachine for deletion")
			return err
		}

		// Delete all associated PVCs
		if err := r.StorageManager.DeleteAssociatedPVCs(ctx, machine, vmName); err != nil {
			logger.Error(err, "Failed to delete associated PVCs")
			return err
		}

		// Remove finalizer
		controllerutil.RemoveFinalizer(machine, MachineFinalizer)
		if err := r.Update(ctx, machine); err != nil {
			logger.Error(err, "Failed to remove finalizer from Machine")
			return err
		}
	}

	return nil
}

// isKubevirtProvider returns true if the Machine's provider is kubevirt.
// If providerConfig.name is empty or not set, we treat it as kubevirt for backward compatibility.
func (r *MachineReconciler) isKubevirtProvider(machine *vitistackv1alpha1.Machine) bool {
	name := r.getProviderName(machine)
	if name == "" {
		return true
	}
	return strings.EqualFold(name, "kubevirt")
}

// getProviderName extracts spec.providerConfig.name if available; otherwise returns empty string.
func (r *MachineReconciler) getProviderName(machine *vitistackv1alpha1.Machine) string {
	if machine == nil {
		return ""
	}
	// ProviderConfig is a value type (CloudProviderConfig); zero value has empty Name
	return strings.TrimSpace(machine.Spec.ProviderConfig.Name)
}

// NewMachineReconciler creates a new MachineReconciler with initialized managers
func NewMachineReconciler(c client.Client, scheme *runtime.Scheme) *MachineReconciler {
	eventsManager := events.NewManager(c)
	macGenerator := macaddress.NewVitistackMacGenerator()
	statusManager := status.NewManager(c, eventsManager)

	return &MachineReconciler{
		Client:         c,
		Scheme:         scheme,
		StorageManager: storage.NewManager(c, scheme),
		VMManager:      vm.NewManager(c, scheme, macGenerator),
		StatusManager:  statusManager,
		EventsManager:  eventsManager,
		NetworkManager: network.NewManager(c),
		MacGenerator:   macGenerator,
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
