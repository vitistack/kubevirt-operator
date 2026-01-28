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
	"time"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	"github.com/vitistack/kubevirt-operator/internal/machine/events"
	"github.com/vitistack/kubevirt-operator/internal/machine/network"
	"github.com/vitistack/kubevirt-operator/internal/machine/status"
	"github.com/vitistack/kubevirt-operator/internal/machine/storage"
	"github.com/vitistack/kubevirt-operator/internal/machine/vm"
	"github.com/vitistack/kubevirt-operator/pkg/clients"
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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// MachineReconciler reconciles a Machine object
type MachineReconciler struct {
	client.Client // Supervisor cluster client
	Scheme        *runtime.Scheme

	// Manager instances for different concerns
	StorageManager    *storage.StorageManager
	VMManager         *vm.VMManager
	StatusManager     *status.StatusManager
	EventsManager     *events.EventsManager
	NetworkManager    *network.NetworkManager
	MacGenerator      macaddress.MacAddressGenerator
	KubevirtClientMgr clients.ClientManager
}

const (
	MachineFinalizer = "machine.vitistack.io/finalizer"
	RequeueDelay     = 5 * time.Second
)

// +kubebuilder:rbac:groups=vitistack.io,resources=machines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vitistack.io,resources=machines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=vitistack.io,resources=machines/finalizers,verbs=update
// +kubebuilder:rbac:groups=vitistack.io,resources=kubevirtconfigs,verbs=get;list;watch
// +kubebuilder:rbac:groups=vitistack.io,resources=networkconfigurations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vitistack.io,resources=networkconfigurations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachines/status,verbs=get
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances,verbs=get;list;watch
// +kubebuilder:rbac:groups=kubevirt.io,resources=virtualmachineinstances/status,verbs=get
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=k8s.cni.cncf.io,resources=network-attachment-definitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=storage.k8s.io,resources=storageclasses,verbs=get;list;watch
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list

func (r *MachineReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	machine, result, stop, err := r.fetchAndInitMachine(ctx, req)
	if stop || err != nil {
		return result, err
	}

	// Get the remote KubeVirt client for this machine
	remoteClient, kubevirtConfigName, needsAnnotationUpdate, err := r.KubevirtClientMgr.GetOrCreateClientFromMachine(ctx, machine)
	if err != nil {
		logger.Error(err, "Failed to get KubeVirt client for machine")
		r.recordKubevirtConfigFailure(ctx, machine, err)
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	// Set the remote client in VMManager for VM operations
	r.VMManager.SetRemoteClient(remoteClient)

	// Ensure the namespace exists on the remote KubeVirt cluster
	if err := r.ensureNamespaceExists(ctx, machine.Namespace, remoteClient); err != nil {
		logger.Error(err, "Failed to ensure namespace exists on remote cluster")
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	// Update machine annotations with kubevirt config reference if needed
	// TODO: Update machine status with kubevirt config reference once field is added to CRD
	if needsAnnotationUpdate {
		// Fetch latest version to avoid conflict
		latestMachine := &vitistackv1alpha1.Machine{}
		if err := r.Get(ctx, types.NamespacedName{Name: machine.Name, Namespace: machine.Namespace}, latestMachine); err != nil {
			logger.Error(err, "Failed to get latest machine version for annotation update")
			return ctrl.Result{}, err
		}
		machine = latestMachine

		if machine.Annotations == nil {
			machine.Annotations = make(map[string]string)
		}
		machine.Annotations["vitistack.io/kubevirt-config"] = kubevirtConfigName
		if err := r.Update(ctx, machine); err != nil {
			logger.Error(err, "Failed to update machine annotations with kubevirt config")
			return ctrl.Result{RequeueAfter: RequeueDelay}, err
		} else {
			logger.V(1).Info("Automatically assigned KubevirtConfig to machine",
				"machine", machine.Name,
				"kubevirtConfig", kubevirtConfigName)
		}
	}

	virtualmachine, vmName, result, stop, err := r.ensureVirtualMachine(ctx, machine, remoteClient)
	if stop || err != nil {
		return result, err
	}

	// Handle ISO cleanup if needed (delete DataVolume/PVC after OS installation)
	// Boot order ensures root disk boots first, so this is purely for freeing storage
	r.handleISOCleanup(ctx, machine, vmName)

	vmi, vmiExists, err := r.getVMI(ctx, vmName, machine.Namespace, remoteClient)
	if err != nil {
		logger.Error(err, "Failed to get VirtualMachineInstance")
		return ctrl.Result{}, err
	}

	// Log VMI status for debugging
	if vmiExists {
		logger.V(1).Info("VMI status",
			"vm", vmName,
			"vmiPhase", vmi.Status.Phase,
			"vmiNodeName", vmi.Status.NodeName)
	} else {
		logger.V(1).Info("VMI does not exist yet", "vm", vmName)
	}

	// Update status from VM/VMI state - this ensures other operators get current info
	result, err = r.StatusManager.UpdateMachineStatusFromVMAndVMI(ctx, machine, virtualmachine, vmi, vmiExists, remoteClient)
	if err != nil {
		logger.Error(err, "Failed to update Machine status from VM/VMI")
	} else {
		logger.V(1).Info("Reconcile completed successfully",
			"machine", machine.Name,
			"phase", machine.Status.Phase,
			"state", machine.Status.State)
	}

	// Always requeue after 5 seconds to keep status fresh for dependent operators
	if result.RequeueAfter == 0 {
		result.RequeueAfter = RequeueDelay
	}

	return result, err
}

// ensureNamespaceExists creates the namespace on the remote cluster if it doesn't exist
func (r *MachineReconciler) ensureNamespaceExists(ctx context.Context, namespaceName string, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	namespace := &corev1.Namespace{}
	err := remoteClient.Get(ctx, types.NamespacedName{Name: namespaceName}, namespace)
	if err == nil {
		// Namespace exists
		logger.V(1).Info("Namespace already exists on remote cluster", "namespace", namespaceName)
		return nil
	}

	if !errors.IsNotFound(err) {
		// Some other error occurred
		return fmt.Errorf("failed to check if namespace exists: %w", err)
	}

	// Namespace doesn't exist, create it
	namespace = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: namespaceName,
			Labels: map[string]string{
				vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
			},
		},
	}

	if err := remoteClient.Create(ctx, namespace); err != nil {
		return fmt.Errorf("failed to create namespace on remote cluster: %w", err)
	}

	logger.Info("Created namespace on remote cluster", "namespace", namespaceName)
	return nil
}

// recordKubevirtConfigFailure updates machine status for kubevirt config errors.
func (r *MachineReconciler) recordKubevirtConfigFailure(ctx context.Context, machine *vitistackv1alpha1.Machine, err error) {
	logger := log.FromContext(ctx)
	reason := "Failed to get or create KubeVirt client from config"
	errorTitle := "KubevirtConfigError"
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

// fetchAndInitMachine retrieves the Machine and handles deletion/finalizer logic.
// Returns: machine, result, stopReconcile, error
func (r *MachineReconciler) fetchAndInitMachine(ctx context.Context, req ctrl.Request) (*vitistackv1alpha1.Machine, ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)
	machine := &vitistackv1alpha1.Machine{}
	if err := r.Get(ctx, req.NamespacedName, machine); err != nil {
		if errors.IsNotFound(err) {
			// This is expected when the resource is deleted. Use V(1) to reduce log noise.
			logger.V(1).Info("Machine resource not found, likely already deleted",
				"name", req.Name,
				"namespace", req.Namespace)
			return nil, ctrl.Result{}, true, nil
		}
		logger.Error(err, "Failed to get Machine")
		return nil, ctrl.Result{}, true, err
	}

	// Note: Provider filtering is done at the predicate level in SetupWithManager,
	// so we only receive events for kubevirt machines here.

	if machine.GetDeletionTimestamp() != nil {
		logger.Info("Machine has deletion timestamp, entering deletion flow",
			"machine", machine.Name,
			"deletionTimestamp", machine.GetDeletionTimestamp())
		result, err := r.handleDeletion(ctx, machine)
		return machine, result, true, err
	}

	if !controllerutil.ContainsFinalizer(machine, MachineFinalizer) {
		controllerutil.AddFinalizer(machine, MachineFinalizer)
		if err := r.Update(ctx, machine); err != nil {
			if errors.IsConflict(err) {
				// Conflict error means the resource was modified, requeue to retry
				logger.V(1).Info("Conflict updating Machine finalizer, requeuing")
				return machine, ctrl.Result{Requeue: true}, true, nil
			}
			logger.Error(err, "Failed to add finalizer to Machine")
			return machine, ctrl.Result{}, true, err
		}
		return machine, ctrl.Result{Requeue: true}, true, nil
	}
	return machine, ctrl.Result{}, false, nil
}

// ensureVirtualMachine fetches or creates the backing VirtualMachine.
// Returns: vm, vmName, result, stopReconcile, error
func (r *MachineReconciler) ensureVirtualMachine(ctx context.Context, machine *vitistackv1alpha1.Machine, remoteClient client.Client) (*kubevirtv1.VirtualMachine, string, ctrl.Result, bool, error) {
	logger := log.FromContext(ctx)
	virtualmachine := &kubevirtv1.VirtualMachine{}
	vmName := machine.Name
	vmKey := types.NamespacedName{Name: vmName, Namespace: machine.Namespace}

	// Get VM from remote KubeVirt cluster
	if err := remoteClient.Get(ctx, vmKey, virtualmachine); err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get VirtualMachine from remote cluster")
			return nil, vmName, ctrl.Result{}, true, err
		}

		logger.Info("VirtualMachine not found, creating new VM", "vm", vmName, "namespace", machine.Namespace)
		pvcNames, pvcErr := r.StorageManager.CreatePVCsFromDiskSpecs(ctx, machine, vmName, remoteClient)
		if pvcErr != nil {
			logger.Error(pvcErr, "Failed to create PVCs")
			_ = r.StatusManager.UpdateMachineStatus(ctx, machine, "Failed")
			return nil, vmName, ctrl.Result{RequeueAfter: RequeueDelay}, true, pvcErr
		}
		newVM, vmErr := r.VMManager.CreateVirtualMachine(ctx, machine, vmName, pvcNames)
		if vmErr != nil {
			logger.Error(vmErr, "Failed to create VirtualMachine in remote cluster")
			_ = r.StatusManager.UpdateMachineStatus(ctx, machine, "Failed")
			return nil, vmName, ctrl.Result{RequeueAfter: RequeueDelay}, true, vmErr
		}
		logger.Info("Created VirtualMachine in remote cluster", "virtualmachine", newVM.Name)
		return newVM, vmName, ctrl.Result{}, false, nil
	}

	// VM already exists, log current status
	logger.V(1).Info("VirtualMachine already exists on remote cluster",
		"vm", vmName,
		"namespace", machine.Namespace,
		"ready", virtualmachine.Status.Ready,
		"created", virtualmachine.Status.Created)
	return virtualmachine, vmName, ctrl.Result{}, false, nil
}

// handleISOCleanup checks if ISO resources should be cleaned up and deletes them if needed.
// This is called after the VM is created/fetched to handle ISO cleanup after OS installation.
// Boot order ensures root disk boots first, so cleanup is purely for freeing storage.
func (r *MachineReconciler) handleISOCleanup(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string) {
	logger := log.FromContext(ctx)

	// Check if ISO cleanup is needed
	if !r.VMManager.ShouldCleanupISO(machine) {
		return
	}

	logger.Info("Cleaning up ISO resources", "vm", vmName)
	if err := r.VMManager.CleanupISOResources(ctx, machine, vmName); err != nil {
		logger.Error(err, "Failed to cleanup ISO resources")
		// Continue anyway, we can retry on next reconcile
		return
	}

	// Mark cleanup as done to avoid repeated attempts
	if machine.Annotations == nil {
		machine.Annotations = make(map[string]string)
	}
	machine.Annotations[vm.AnnotationISOCleanedUp] = vm.AnnotationValueTrue

	// Update the Machine with the cleanup annotation
	if err := r.Update(ctx, machine); err != nil {
		logger.Error(err, "Failed to mark ISO as cleaned up")
	}

	logger.Info("ISO resources cleaned up successfully", "vm", vmName)
}

// getVMI retrieves the VMI if it exists from the remote cluster.
func (r *MachineReconciler) getVMI(ctx context.Context, vmName, namespace string, remoteClient client.Client) (*kubevirtv1.VirtualMachineInstance, bool, error) {
	vmi := &kubevirtv1.VirtualMachineInstance{}
	key := types.NamespacedName{Name: vmName, Namespace: namespace}
	if err := remoteClient.Get(ctx, key, vmi); err != nil {
		if errors.IsNotFound(err) {
			return vmi, false, nil
		}
		return nil, false, err
	}
	return vmi, true, nil
}

func (r *MachineReconciler) handleDeletion(ctx context.Context, machine *vitistackv1alpha1.Machine) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("=== MACHINE DELETION STARTED ===",
		"machine", machine.Name,
		"namespace", machine.Namespace,
		"hasFinalizer", controllerutil.ContainsFinalizer(machine, MachineFinalizer))

	if !controllerutil.ContainsFinalizer(machine, MachineFinalizer) {
		logger.Info("No finalizer present, skipping cleanup")
		return ctrl.Result{}, nil
	}

	logger.Info("Getting remote KubeVirt client for cleanup")
	// Get the remote KubeVirt client for cleanup operations
	remoteClient, _, _, err := r.KubevirtClientMgr.GetOrCreateClientFromMachine(ctx, machine)
	if err != nil {
		logger.Error(err, "Failed to get KubeVirt client for deletion")
		// Cannot proceed without remote client - PVCs and VM are on remote cluster
		// Requeue to retry the deletion
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	logger.Info("Remote client obtained, starting cleanup of remote resources")
	// Cleanup resources on remote cluster
	result, err := r.cleanupRemoteResources(ctx, machine, remoteClient)
	if err != nil || result.RequeueAfter > 0 {
		logger.Info("Remote cleanup needs retry or returned error",
			"error", err,
			"requeue", result.RequeueAfter > 0)
		return result, err
	}

	logger.Info("Remote cleanup completed, cleaning up network configuration")

	// Cleanup network configuration from supervisor cluster
	if err := r.VMManager.CleanupNetworkConfiguration(ctx, machine); err != nil {
		logger.Error(err, "Failed to cleanup network configuration")
		return ctrl.Result{}, err
	}

	// Remove finalizer
	if err := r.removeMachineFinalizer(ctx, machine); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// cleanupRemoteResources handles cleanup of VM, PVCs, and NAD on the remote KubeVirt cluster
func (r *MachineReconciler) cleanupRemoteResources(ctx context.Context, machine *vitistackv1alpha1.Machine, remoteClient client.Client) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	logger.Info("=== STARTING CLEANUP OF REMOTE RESOURCES ===",
		"machine", machine.Name,
		"namespace", machine.Namespace)

	r.VMManager.SetRemoteClient(remoteClient)
	vmName := machine.Name
	vmKey := types.NamespacedName{Name: vmName, Namespace: machine.Namespace}

	nadName, err := r.resolveNADNameForDeletion(ctx, machine, vmKey, remoteClient)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.StorageManager.DeleteAssociatedPVCs(ctx, machine, vmName, remoteClient); err != nil {
		logger.Error(err, "Failed to delete associated PVCs")
		return ctrl.Result{}, err
	}
	logger.Info("=== PVC DELETION COMPLETED ===")

	r.maybeCleanupNADBeforeVMDeletion(ctx, nadName, machine.Namespace, vmName, remoteClient)

	logger.Info("=== STEP 2: DELETING VIRTUALMACHINE ===", "vm", vmName, "namespace", machine.Namespace)
	if _, err := r.deleteVirtualMachine(ctx, vmKey, remoteClient); err != nil {
		logger.Error(err, "Failed to delete VirtualMachine")
		return ctrl.Result{}, err
	}

	vmStillExists, err := r.virtualMachineExists(ctx, vmKey, remoteClient)
	if err != nil {
		logger.Error(err, "Failed to verify VirtualMachine deletion")
		return ctrl.Result{}, err
	}
	if vmStillExists {
		logger.Info("VM still exists - requeuing (PVCs cleaned up, will retry for NAD)",
			"virtualmachine", vmKey.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	r.finalizeNADCleanup(ctx, machine, nadName, remoteClient)

	logger.Info("=== CLEANUP COMPLETED SUCCESSFULLY ===")
	return ctrl.Result{}, nil
}

func (r *MachineReconciler) resolveNADNameForDeletion(ctx context.Context, machine *vitistackv1alpha1.Machine, vmKey types.NamespacedName, remoteClient client.Client) (string, error) {
	logger := log.FromContext(ctx)

	if machine.Annotations != nil {
		if nadName := machine.Annotations["vitistack.io/nad-to-cleanup"]; nadName != "" {
			logger.Info("NAD annotation check", "nadName", nadName, "hasAnnotation", true)
			return nadName, nil
		}
	}

	logger.Info("NAD annotation empty, attempting to extract from VM")
	virtualMachine := &kubevirtv1.VirtualMachine{}
	if err := remoteClient.Get(ctx, vmKey, virtualMachine); err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}

	nadName := r.extractNADNameFromVM(virtualMachine)
	if nadName == "" {
		return "", nil
	}
	logger.Info("Extracted NAD name from VM", "nadName", nadName)

	if machine.Annotations == nil {
		machine.Annotations = make(map[string]string)
	}
	machine.Annotations["vitistack.io/nad-to-cleanup"] = nadName
	if err := r.Update(ctx, machine); err != nil {
		logger.Error(err, "Failed to store NAD name in annotations")
	}

	return nadName, nil
}

func (r *MachineReconciler) maybeCleanupNADBeforeVMDeletion(ctx context.Context, nadName, namespace, vmName string, remoteClient client.Client) {
	logger := log.FromContext(ctx)
	if nadName == "" {
		logger.Info("No NAD name found, skipping specific NAD cleanup")
		return
	}

	logger.Info("Checking if NAD can be removed before VM deletion", "nadName", nadName)
	if err := r.NetworkManager.CleanupNADIfUnusedByOtherVMs(ctx, nadName, namespace, vmName, remoteClient); err != nil {
		logger.Error(err, "Failed to perform pre-VM NAD cleanup")
	}
}

func (r *MachineReconciler) virtualMachineExists(ctx context.Context, vmKey types.NamespacedName, remoteClient client.Client) (bool, error) {
	virtualMachine := &kubevirtv1.VirtualMachine{}
	if err := remoteClient.Get(ctx, vmKey, virtualMachine); err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (r *MachineReconciler) finalizeNADCleanup(ctx context.Context, machine *vitistackv1alpha1.Machine, nadName string, remoteClient client.Client) {
	logger := log.FromContext(ctx)
	if nadName != "" {
		if err := r.NetworkManager.CleanupNetworkAttachmentDefinition(ctx, nadName, machine.Namespace, remoteClient); err != nil {
			logger.Error(err, "Failed to cleanup NetworkAttachmentDefinition",
				"nad", nadName,
				"namespace", machine.Namespace)
		}
	} else {
		logger.Info("No NAD name found, skipping specific NAD cleanup")
	}

	logger.Info("Running orphaned NAD cleanup", "namespace", machine.Namespace)
	if err := r.NetworkManager.CleanupOrphanedNADs(ctx, machine.Namespace, remoteClient); err != nil {
		logger.Error(err, "Failed to cleanup orphaned NADs", "namespace", machine.Namespace)
	}

	if nadName != "" && machine.Annotations != nil {
		delete(machine.Annotations, "vitistack.io/nad-to-cleanup")
		if err := r.Update(ctx, machine); err != nil {
			logger.Error(err, "Failed to remove NAD annotation")
		}
	}
}

// deleteVirtualMachine deletes the VM and returns the NAD name if found
func (r *MachineReconciler) deleteVirtualMachine(ctx context.Context, vmNamespacedName types.NamespacedName, remoteClient client.Client) (string, error) {
	logger := log.FromContext(ctx)

	virtualMachine := &kubevirtv1.VirtualMachine{}
	err := remoteClient.Get(ctx, vmNamespacedName, virtualMachine)
	if err != nil {
		if errors.IsNotFound(err) {
			logger.Info("VirtualMachine not found, nothing to delete")
			return "", nil
		}
		logger.Error(err, "Failed to get VirtualMachine for deletion from remote cluster")
		return "", err
	}

	// Extract NAD name from VM networks before deletion
	nadName := r.extractNADNameFromVM(virtualMachine)

	logger.Info("Found VirtualMachine, preparing for deletion",
		"vm", virtualMachine.Name,
		"running", virtualMachine.Spec.Running,
		"runStrategy", virtualMachine.Spec.RunStrategy)

	// Stop the VM if it's running
	if virtualMachine.Spec.Running != nil && *virtualMachine.Spec.Running {
		logger.Info("VM is running, stopping it first")
		running := false
		virtualMachine.Spec.Running = &running
		if err := remoteClient.Update(ctx, virtualMachine); err != nil {
			logger.Error(err, "Failed to stop VirtualMachine")
			return nadName, err
		}
		logger.Info("VM stop initiated")
	}

	// VirtualMachine exists, delete it from remote cluster
	logger.Info("Deleting VirtualMachine from remote cluster", "vm", virtualMachine.Name)
	if err := remoteClient.Delete(ctx, virtualMachine); err != nil {
		if errors.IsNotFound(err) {
			logger.Info("VirtualMachine already deleted")
			return nadName, nil
		}
		logger.Error(err, "Failed to delete VirtualMachine from remote cluster")
		return "", err
	}
	logger.Info("VirtualMachine deletion request sent successfully", "virtualmachine", virtualMachine.Name)

	return nadName, nil
}

// removeMachineFinalizer fetches the latest machine and removes the finalizer
func (r *MachineReconciler) removeMachineFinalizer(ctx context.Context, machine *vitistackv1alpha1.Machine) error {
	logger := log.FromContext(ctx)

	// Fetch the latest version of the machine before removing finalizer
	latestMachine := &vitistackv1alpha1.Machine{}
	if err := r.Get(ctx, types.NamespacedName{Name: machine.Name, Namespace: machine.Namespace}, latestMachine); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("Machine already deleted, cleanup complete")
			return nil
		}
		logger.Error(err, "Failed to get latest Machine before finalizer removal")
		return err
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(latestMachine, MachineFinalizer)
	if err := r.Update(ctx, latestMachine); err != nil {
		if errors.IsConflict(err) {
			logger.V(1).Info("Conflict removing finalizer during deletion, will retry")
			return err
		}
		logger.Error(err, "Failed to remove finalizer from Machine")
		return err
	}

	return nil
}

// extractNADNameFromVM extracts the NetworkAttachmentDefinition name from a VirtualMachine
func (r *MachineReconciler) extractNADNameFromVM(virtualMachine *kubevirtv1.VirtualMachine) string {
	if virtualMachine.Spec.Template == nil {
		return ""
	}

	// Look for Multus networks in the VM spec
	for i := range virtualMachine.Spec.Template.Spec.Networks {
		net := &virtualMachine.Spec.Template.Spec.Networks[i]
		if net.Multus != nil && net.Multus.NetworkName != "" {
			return net.Multus.NetworkName
		}
	}

	return ""
}

// isKubevirtProvider returns true if the Machine's provider is kubevirt.
// If providerConfig.name is empty or not set, we treat it as kubevirt for backward compatibility.
func (r *MachineReconciler) isKubevirtProvider(machine *vitistackv1alpha1.Machine) bool {
	provider := r.getProviderName(machine)
	if provider == "" {
		return true
	}
	return provider == vitistackv1alpha1.MachineProviderTypeKubevirt
}

// getProviderName extracts spec.providerConfig.name if available; otherwise returns empty string.
func (r *MachineReconciler) getProviderName(machine *vitistackv1alpha1.Machine) vitistackv1alpha1.MachineProviderType {
	if machine == nil {
		return ""
	}
	// ProviderConfig is a value type (CloudProviderConfig); zero value has empty Name
	return machine.Spec.Provider
}

// NewMachineReconciler creates a new MachineReconciler with initialized managers
func NewMachineReconciler(c client.Client, scheme *runtime.Scheme, kubevirtClientMgr clients.ClientManager) *MachineReconciler {
	eventsManager := events.NewManager(c)
	macGenerator := macaddress.NewVitistackMacGenerator()
	statusManager := status.NewManager(c, eventsManager)

	return &MachineReconciler{
		Client:            c,
		Scheme:            scheme,
		StorageManager:    storage.NewManager(c, scheme),
		VMManager:         vm.NewManager(c, scheme, macGenerator, statusManager),
		StatusManager:     statusManager,
		EventsManager:     eventsManager,
		NetworkManager:    network.NewManager(c),
		MacGenerator:      macGenerator,
		KubevirtClientMgr: kubevirtClientMgr,
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *MachineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create predicate to filter only KubeVirt machines
	// This prevents the reconciler from being triggered for machines with other providers
	kubevirtMachinePredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			if machine, ok := e.Object.(*vitistackv1alpha1.Machine); ok {
				return r.isKubevirtProvider(machine)
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if machine, ok := e.ObjectNew.(*vitistackv1alpha1.Machine); ok {
				return r.isKubevirtProvider(machine)
			}
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			if machine, ok := e.Object.(*vitistackv1alpha1.Machine); ok {
				return r.isKubevirtProvider(machine)
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			if machine, ok := e.Object.(*vitistackv1alpha1.Machine); ok {
				return r.isKubevirtProvider(machine)
			}
			return false
		},
	}

	// In a multi-cluster architecture, we only watch Machine resources on the supervisor cluster.
	// VirtualMachine and VirtualMachineInstance resources exist on remote KubeVirt clusters,
	// not on the supervisor cluster, so we don't set up watches for them here.
	// Instead, we interact with them directly through the remote clients in the reconciliation loop.
	return ctrl.NewControllerManagedBy(mgr).
		For(&vitistackv1alpha1.Machine{}).
		WithEventFilter(kubevirtMachinePredicate).
		Named("kubevirt-machine").
		Complete(r)
}
