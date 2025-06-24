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

package events

import (
	"context"
	"fmt"
	"strings"

	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// EventsManager handles event monitoring and error detection
type EventsManager struct {
	client.Client
}

// NewManager creates a new events manager
func NewManager(c client.Client) *EventsManager {
	return &EventsManager{
		Client: c,
	}
}

// GetVMIEvents fetches events related to a VirtualMachineInstance and returns error events
func (m *EventsManager) GetVMIEvents(ctx context.Context, vmi *kubevirtv1.VirtualMachineInstance, machine *vitistackv1alpha1.Machine) ([]string, error) {
	logger := log.FromContext(ctx)

	// Get events for the VMI
	eventList := &corev1.EventList{}
	listOpts := &client.ListOptions{
		Namespace: vmi.Namespace,
	}

	if err := m.List(ctx, eventList, listOpts); err != nil {
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

// GetVMEvents fetches events related to a VirtualMachine and returns error events
func (m *EventsManager) GetVMEvents(ctx context.Context, vm *kubevirtv1.VirtualMachine, machine *vitistackv1alpha1.Machine) ([]string, error) {
	logger := log.FromContext(ctx)

	// Get events for the VM
	eventList := &corev1.EventList{}
	listOpts := &client.ListOptions{
		Namespace: vm.Namespace,
	}

	if err := m.List(ctx, eventList, listOpts); err != nil {
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
