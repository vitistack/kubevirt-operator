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
	"strings"
	"time"

	"net"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/machine/events"
	"github.com/vitistack/kubevirt-operator/pkg/consts"
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
		machine.ResourceVersion = latestMachine.ResourceVersion
		return nil
	}

	return fmt.Errorf("failed to update Machine status after %d retries", maxRetries)
}

// UpdateMachineStatusFromVMAndVMI determines and updates machine status based on VM and VMI state
func (m *StatusManager) UpdateMachineStatusFromVMAndVMI(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine, vmi *kubevirtv1.VirtualMachineInstance, vmiExists bool, remoteClient client.Client) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	state, phase, requeue := m.evaluateState(machine, vm, vmi, vmiExists, ctx, remoteClient)

	machine.Status.Phase = phase
	machine.Status.State = state
	machine.Status.Provider = "kubevirt"

	if err := m.UpdateMachineStatus(ctx, machine, state); err != nil {
		logger.Error(err, "Failed to update Machine status")
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

// evaluateState determines the machine state/phase and performs ancillary updates (pod info & error checks)
func (m *StatusManager) evaluateState(machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine, vmi *kubevirtv1.VirtualMachineInstance, vmiExists bool, ctx context.Context, remoteClient client.Client) (state, phase string, requeue bool) {
	logger := log.FromContext(ctx)
	if vmiExists {
		state, phase, requeue = m.mapVMIPhase(vmi.Status.Phase)
		if vmi.Status.Phase == kubevirtv1.Running {
			if err := m.updateStatusFromPodVMVMIStatus(ctx, machine, vmi, remoteClient); err != nil {
				logger.Error(err, "Failed to update vm and vmi status information", "vmi", vmi.Name)
			}
		}
		if err := m.checkVMIErrorsAndUpdateStatus(ctx, machine, vmi, remoteClient); err != nil {
			logger.Error(err, "Failed to check VMI errors")
		}
		for i := range vmi.Status.Conditions {
			c := vmi.Status.Conditions[i]
			logger.Info("VMI condition", "type", c.Type, "status", c.Status, "reason", c.Reason)
		}
		return
	}
	// VM path (no VMI yet)
	state, phase, requeue = m.mapVMStatus(vm)
	if err := m.checkVMErrorsAndUpdateStatus(ctx, machine, vm, remoteClient); err != nil {
		logger.Error(err, "Failed to check VM errors")
	}
	return
}

func (m *StatusManager) mapVMIPhase(phase kubevirtv1.VirtualMachineInstancePhase) (state, mappedPhase string, requeue bool) {
	switch phase {
	case kubevirtv1.Running:
		return consts.MachineStateRunning, consts.MachinePhaseRunning, false
	case kubevirtv1.Pending:
		return consts.MachineStatePending, consts.MachinePhasePending, true
	case kubevirtv1.Scheduling:
		return consts.MachineStateScheduling, consts.MachinePhaseScheduling, true
	case kubevirtv1.Scheduled:
		return consts.MachineStateScheduled, consts.MachinePhaseScheduled, true
	case kubevirtv1.Succeeded:
		return consts.MachineStateSucceeded, consts.MachinePhaseSucceeded, false
	case kubevirtv1.Failed:
		return consts.MachineStateFailed, consts.MachinePhaseFailed, false
	default:
		return consts.MachineStateUnknown, consts.MachinePhaseUnknown, true
	}
}

func (m *StatusManager) mapVMStatus(vm *kubevirtv1.VirtualMachine) (state, phase string, requeue bool) {
	if vm.Status.Ready {
		return consts.MachineStateRunning, consts.MachinePhaseRunning, false
	}
	if vm.Status.Created {
		return consts.MachineStatePending, consts.MachinePhasePending, true
	}
	return consts.MachineStatePending, consts.MachinePhasePending, true
}

// checkVMIErrorsAndUpdateStatus checks for VMI errors and updates machine status accordingly
func (m *StatusManager) checkVMIErrorsAndUpdateStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, vmi *kubevirtv1.VirtualMachineInstance, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	// Get VMI events from the remote cluster to check for errors
	errorEvents, err := m.EventsManager.GetVMIEvents(ctx, vmi, machine, remoteClient)
	if err != nil {
		logger.Error(err, "Failed to get VMI events")
		return err
	}

	// If there are error events and VMI is in a problematic state, update status
	if len(errorEvents) > 0 {
		switch vmi.Status.Phase {
		case kubevirtv1.Pending, kubevirtv1.Failed, kubevirtv1.Scheduling, kubevirtv1.Scheduled:

			machine.Status.State = consts.MachineStateFailed
			machine.Status.Phase = consts.MachineStateFailed

			// Update the status field with error information
			errorSummary := fmt.Sprintf("VM startup failed - %d error(s) found: %s",
				len(errorEvents),
				strings.Join(errorEvents, "; "))

			// Truncate if too long to avoid status field size limits
			if len(errorSummary) > 500 {
				errorSummary = errorSummary[:497] + "..."
			}

			// Store the error details in status fields that are available in the CRD
			machine.Status.State = consts.MachineStateFailed
			machine.Status.Phase = consts.MachineStateFailed

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
	}

	return nil
}

// checkVMErrorsAndUpdateStatus checks for VM errors and updates machine status accordingly
func (m *StatusManager) checkVMErrorsAndUpdateStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	// Get VM events from the remote cluster to check for errors
	errorEvents, err := m.EventsManager.GetVMEvents(ctx, vm, machine, remoteClient)
	if err != nil {
		logger.Error(err, "Failed to get VM events")
		return err
	}

	// If there are error events and VM is not ready, update status
	if len(errorEvents) > 0 && !vm.Status.Ready {
		machine.Status.State = consts.MachineStateFailed
		machine.Status.Phase = consts.MachineStateFailed

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

// updateStatusFromPodVMVMIStatus fetches the Pod created by the VMI from the remote cluster and extracts HostIP and PodIP, and update status from VM
func (m *StatusManager) updateStatusFromPodVMVMIStatus(ctx context.Context, machine *vitistackv1alpha1.Machine, vmi *kubevirtv1.VirtualMachineInstance, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	// Get all Pods in the VMI's namespace from the remote cluster and find one that contains the VMI name
	// KubeVirt creates Pods with names that contain the VMI name but may have additional suffixes
	podList := &corev1.PodList{}
	if err := remoteClient.List(ctx, podList, client.InNamespace(vmi.Namespace)); err != nil {
		return fmt.Errorf("failed to list Pods in namespace %s from remote cluster: %w", vmi.Namespace, err)
	}

	var matchedPod *corev1.Pod
	for i := range podList.Items {
		pod := &podList.Items[i]
		if pod.Labels == nil {
			// If there are no labels, we can't determine if this is a KubeVirt Pod
			continue
		}

		value, ok := pod.Labels["source-machine"]
		if !ok {
			continue
		}

		if value == machine.Name {
			matchedPod = pod
			break
		}
	}

	if matchedPod == nil {
		logger.Info("No Pod found for VMI", "vmi", vmi.Name, "namespace", vmi.Namespace)
		return nil // Not an error - Pod might not be created yet
	}

	logger.Info("Found Pod for VMI", "pod", matchedPod.Name, "namespace", matchedPod.Namespace, "hostIP", matchedPod.Status.HostIP, "podIP", matchedPod.Status.PodIP)

	networkInterfaces := []vitistackv1alpha1.NetworkInterfaceStatus{}
	networkInterfaces = extractNetworkInterfacesFromVMI(vmi, networkInterfaces)

	privateIpAddresses := []string{}
	for i := range networkInterfaces {
		networkInterface := &networkInterfaces[i]
		privateIpAddresses = append(privateIpAddresses, networkInterface.IPAddresses...)
	}

	machine.Status.PrivateIPAddresses = privateIpAddresses
	machine.Status.NetworkInterfaces = networkInterfaces

	diskVolumes, err := extractDiskVolumesFromVMI(vmi)
	if err != nil {
		return err
	}

	machine.Status.Disks = diskVolumes
	return nil
}

func extractNetworkInterfacesFromVMI(vmi *kubevirtv1.VirtualMachineInstance, networkInterfaces []vitistackv1alpha1.NetworkInterfaceStatus) []vitistackv1alpha1.NetworkInterfaceStatus {
	for i := range vmi.Status.Interfaces {
		networkInterface := &vmi.Status.Interfaces[i]
		ipv4Addresses := []string{}
		ipv6Addresses := []string{}
		for j := range networkInterface.IPs {
			ip := networkInterface.IPs[j]
			if parsedIP := net.ParseIP(ip); parsedIP != nil {
				if parsedIP.To4() != nil {
					ipv4Addresses = append(ipv4Addresses, ip)
				} else {
					ipv6Addresses = append(ipv6Addresses, ip)
				}
			} else {
				fmt.Printf("%s is not a valid IP address\n", ip)
			}
		}

		if networkInterface.MAC == "" || networkInterface.MAC == "00:00:00:00:00:00" {
			continue
		}

		if len(ipv4Addresses) == 0 && len(ipv6Addresses) == 0 {
			continue
		}

		networkInterfaces = append(networkInterfaces, vitistackv1alpha1.NetworkInterfaceStatus{
			Name:          networkInterface.Name,
			MACAddress:    networkInterface.MAC,
			IPAddresses:   ipv4Addresses,
			IPv6Addresses: ipv6Addresses,
			State:         networkInterface.LinkState,
			Type:          networkInterface.InfoSource,
		})
	}
	return networkInterfaces
}

func extractDiskVolumesFromVMI(vmi *kubevirtv1.VirtualMachineInstance) ([]vitistackv1alpha1.MachineStatusDisk, error) {
	var diskVolumes []vitistackv1alpha1.MachineStatusDisk
	for i := range vmi.Status.VolumeStatus {
		volume := &vmi.Status.VolumeStatus[i]
		if volume.Name != "" {

			diskSize, ok := volume.PersistentVolumeClaimInfo.Capacity.Storage().AsInt64()
			if !ok {
				return nil, fmt.Errorf("failed to get disk size for volume %s", volume.Name)
			}

			accessModes := []string{}
			for j := range volume.PersistentVolumeClaimInfo.AccessModes {
				mode := volume.PersistentVolumeClaimInfo.AccessModes[j]
				accessModes = append(accessModes, string(mode))
			}

			diskVolumes = append(diskVolumes, vitistackv1alpha1.MachineStatusDisk{
				Name:        volume.Name,
				Device:      fmt.Sprintf("/dev/%s", volume.Target),
				PVCName:     volume.PersistentVolumeClaimInfo.ClaimName,
				VolumeMode:  string(*volume.PersistentVolumeClaimInfo.VolumeMode),
				Size:        diskSize,
				AccessModes: accessModes,
			})
		}
	}
	return diskVolumes, nil
}
