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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// GetVMIEvents fetches events related to a VirtualMachineInstance and returns error events from the remote cluster
func (m *EventsManager) GetVMIEvents(ctx context.Context, vmi *kubevirtv1.VirtualMachineInstance, machine *vitistackv1alpha1.Machine, remoteClient client.Client) ([]string, error) {
	return m.collectErrorEvents(ctx, vmi.Namespace, vmi.Name, string(vmi.UID), "VirtualMachineInstance", vmi.CreationTimestamp, machine.CreationTimestamp, true, remoteClient)
}

// GetVMEvents fetches events related to a VirtualMachine and returns error events from the remote cluster
func (m *EventsManager) GetVMEvents(ctx context.Context, vm *kubevirtv1.VirtualMachine, machine *vitistackv1alpha1.Machine, remoteClient client.Client) ([]string, error) {
	return m.collectErrorEvents(ctx, vm.Namespace, vm.Name, string(vm.UID), "VirtualMachine", vm.CreationTimestamp, machine.CreationTimestamp, false, remoteClient)
}

// collectErrorEvents consolidates event collection and filtering logic for both VMIs and VMs from the remote cluster
func (m *EventsManager) collectErrorEvents(ctx context.Context, namespace, name, uid, kind string, objectCreation, machineCreation metav1.Time, includeSyncFailed bool, remoteClient client.Client) ([]string, error) {
	logger := log.FromContext(ctx)

	// List events from the remote KubeVirt cluster
	eventList := &corev1.EventList{}
	if err := remoteClient.List(ctx, eventList, &client.ListOptions{Namespace: namespace}); err != nil {
		logger.Error(err, "Failed to list events from remote cluster", "kind", kind, "name", name)
		return nil, err
	}

	errorEvents := []string{}
	for i := range eventList.Items { // index loop to avoid copying large structs
		event := &eventList.Items[i]

		// Fast pre-filter: involved object matching
		if event.InvolvedObject.Name != name || event.InvolvedObject.Kind != kind || string(event.InvolvedObject.UID) != uid {
			continue
		}

		// Filter out stale events
		if event.FirstTimestamp.Before(&machineCreation) {
			continue
		}
		if !objectCreation.IsZero() && event.FirstTimestamp.Before(&objectCreation) {
			continue
		}

		if !isErrorEvent(event, includeSyncFailed) {
			continue
		}

		timestamp := formatEventTimestamp(event)
		errorMsg := fmt.Sprintf("[%s] %s: %s", timestamp, event.Reason, event.Message)
		errorEvents = append(errorEvents, errorMsg)

		logger.Info("Found error event",
			"kind", kind,
			"name", name,
			"objectUID", uid,
			"eventUID", event.InvolvedObject.UID,
			"reason", event.Reason,
			"message", event.Message,
			"type", event.Type,
			"eventTime", event.FirstTimestamp)
	}
	return errorEvents, nil
}

func isErrorEvent(event *corev1.Event, includeSyncFailed bool) bool {
	if event.Type == "Warning" { // quick accept
		return true
	}
	lr := strings.ToLower(event.Reason)
	lm := strings.ToLower(event.Message)
	if strings.Contains(lr, "error") || strings.Contains(lr, "failed") ||
		strings.Contains(lm, "error") || strings.Contains(lm, "failed") {
		return true
	}
	if includeSyncFailed && strings.Contains(lm, "syncfailed") {
		return true
	}
	return false
}

func formatEventTimestamp(event *corev1.Event) string {
	if !event.LastTimestamp.IsZero() {
		return event.LastTimestamp.Format("15:04:05")
	}
	if !event.FirstTimestamp.IsZero() {
		return event.FirstTimestamp.Format("15:04:05")
	}
	return "unknown"
}
