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

package vm

import (
	"context"
	"fmt"
	"math"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// safeIntToUint32 safely converts an int to uint32, returning the default value
// if the input is negative or exceeds uint32 max value
func safeIntToUint32(val int, defaultVal uint32) uint32 {
	if val <= 0 || val > math.MaxUint32 {
		return defaultVal
	}
	return uint32(val) // #nosec G115 -- bounds checked above
}

// safeUintToUint32 safely converts a uint to uint32, returning the default value
// if the input exceeds uint32 max value
func safeUintToUint32(val uint, defaultVal uint32) uint32 {
	if val > math.MaxUint32 {
		return defaultVal
	}
	return uint32(val) // #nosec G115 -- bounds checked above
}

// defaultMachineClassValues is used as fallback when no MachineClass is specified
var defaultMachineClassValues = struct {
	Memory  string
	Cores   uint32
	Sockets uint32
	Threads uint32
}{
	Memory:  "2Gi",
	Cores:   2,
	Sockets: 1,
	Threads: 1,
}

// getMachineClass fetches the MachineClass from the supervisor cluster by name.
// Returns the MachineClass if found and enabled, otherwise returns an error.
func (m *VMManager) getMachineClass(ctx context.Context, machineClassName string) (*vitistackv1alpha1.MachineClass, error) {
	if machineClassName == "" {
		return nil, fmt.Errorf("machineClass name is empty")
	}

	machineClass := &vitistackv1alpha1.MachineClass{}
	// MachineClass is cluster-scoped, so no namespace is needed
	err := m.supervisorClient.Get(ctx, client.ObjectKey{Name: machineClassName}, machineClass)
	if err != nil {
		return nil, fmt.Errorf("failed to get MachineClass '%s': %w", machineClassName, err)
	}

	if !machineClass.Spec.Enabled {
		return nil, fmt.Errorf("MachineClass '%s' is not enabled", machineClassName)
	}

	return machineClass, nil
}

// recordMachineClassError updates machine status when MachineClass lookup fails
func (m *VMManager) recordMachineClassError(ctx context.Context, machine *vitistackv1alpha1.Machine, err error) {
	logger := log.FromContext(ctx)
	reason := fmt.Sprintf("Failed to get MachineClass '%s'", machine.Spec.MachineClass)
	errorTitle := "MachineClassError"
	logger.Error(err, reason)

	if machine.Status.Phase != vitistackv1alpha1.MachinePhaseFailed {
		machine.Status.Conditions = append(machine.Status.Conditions, vitistackv1alpha1.MachineCondition{
			Type:    vitistackv1alpha1.ConditionUnknown,
			Status:  string(metav1.ConditionFalse),
			Reason:  reason,
			Message: err.Error(),
		})
	}
	machine.Status.Phase = vitistackv1alpha1.MachinePhaseFailed
	machine.Status.LastUpdated = metav1.Now()
	errMsg := err.Error()
	machine.Status.FailureMessage = &errMsg
	machine.Status.FailureReason = &reason

	if statusErr := m.StatusManager.UpdateMachineStatus(ctx, machine, errorTitle); statusErr != nil {
		logger.Error(statusErr, errorTitle)
	}
}

// recordNetworkFailure updates machine status for network configuration errors.
func (m *VMManager) recordNetworkFailure(ctx context.Context, machine *vitistackv1alpha1.Machine, err error) {
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
	if statusErr := m.StatusManager.UpdateMachineStatus(ctx, machine, errorTitle); statusErr != nil {
		logger.Error(statusErr, errorTitle)
	}
}

// calculateResourceRequirements determines CPU and memory requirements based on machine spec and MachineClass
func (m *VMManager) calculateResourceRequirements(ctx context.Context, machine *vitistackv1alpha1.Machine) (
	memoryRequest string,
	coresRequest uint32,
	socketsRequest uint32,
	threadsRequest uint32,
	err error) {

	// Start with default values
	memoryRequest = defaultMachineClassValues.Memory
	coresRequest = defaultMachineClassValues.Cores
	socketsRequest = defaultMachineClassValues.Sockets
	threadsRequest = defaultMachineClassValues.Threads

	// Try to get MachineClass from supervisor cluster
	if machine.Spec.MachineClass != "" {
		machineClass, mcErr := m.getMachineClass(ctx, machine.Spec.MachineClass)
		if mcErr != nil {
			m.recordMachineClassError(ctx, machine, mcErr)
			return "", 0, 0, 0, mcErr
		}

		// Use MachineClass values
		memoryRequest = machineClass.Spec.Memory.Quantity.String()
		coresRequest = safeUintToUint32(machineClass.Spec.CPU.Cores, defaultMachineClassValues.Cores)
		if machineClass.Spec.CPU.Sockets > 0 {
			socketsRequest = safeUintToUint32(machineClass.Spec.CPU.Sockets, defaultMachineClassValues.Sockets)
		}
		if machineClass.Spec.CPU.Threads > 0 {
			threadsRequest = safeUintToUint32(machineClass.Spec.CPU.Threads, defaultMachineClassValues.Threads)
		}
	}

	// Override with spec values if provided (allows custom overrides per machine)
	if machine.Spec.Memory > 0 {
		memoryRequest = fmt.Sprintf("%dMi", machine.Spec.Memory/1024/1024) // Convert bytes to MiB
	}
	if machine.Spec.CPU.Cores > 0 {
		coresRequest = safeIntToUint32(machine.Spec.CPU.Cores, coresRequest)
	}
	if machine.Spec.CPU.Sockets > 0 {
		socketsRequest = safeIntToUint32(machine.Spec.CPU.Sockets, socketsRequest)
	}
	if machine.Spec.CPU.ThreadsPerCore > 0 {
		threadsRequest = safeIntToUint32(machine.Spec.CPU.ThreadsPerCore, threadsRequest)
	}

	return memoryRequest, coresRequest, socketsRequest, threadsRequest, nil
}
