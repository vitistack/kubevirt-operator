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

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// CDROM volume management constants
const (
	// CDROMVolumeName is the name of the CDROM volume used for ISO boot
	CDROMVolumeName = "cdrom-iso"

	// AnnotationCDROMEjected indicates the CDROM has been removed after installation
	AnnotationCDROMEjected = "vitistack.io/cdrom-ejected"

	// AnnotationBootSource indicates the boot source type (from Machine spec)
	AnnotationBootSource = "kubevirt.io/boot-source"

	// BootSourceDataVolume indicates the VM boots from a DataVolume (ISO)
	BootSourceDataVolume = "datavolume"

	// AnnotationValueTrue is the string value for true annotations
	AnnotationValueTrue = "true"
)

// ShouldRemoveCDROM determines if the CDROM volume should be removed from the VM.
// The CDROM should be removed when:
// 1. The VM was created with an ISO boot source (imageID is set)
// 2. The VM is now running successfully (phase is Running)
// 3. The CDROM hasn't already been ejected
func (m *VMManager) ShouldRemoveCDROM(machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) bool {
	// Check if machine was created with ISO boot
	if machine.Spec.OS.ImageID == "" {
		return false
	}

	// Check if CDROM was already ejected
	if machine.Annotations != nil && machine.Annotations[AnnotationCDROMEjected] == AnnotationValueTrue {
		return false
	}

	// Check if VM exists and has the cdrom-iso volume
	if vm == nil || vm.Spec.Template == nil {
		return false
	}

	hasCDROM := false
	for i := range vm.Spec.Template.Spec.Volumes {
		if vm.Spec.Template.Spec.Volumes[i].Name == CDROMVolumeName {
			hasCDROM = true
			break
		}
	}

	if !hasCDROM {
		return false
	}

	// Check if VMI is running - we only eject after successful boot from disk
	// This is checked by looking at the VM's status
	if vm.Status.PrintableStatus != kubevirtv1.VirtualMachineStatusRunning {
		return false
	}

	// Additional check: verify the VM is ready (successfully booted)
	if !vm.Status.Ready {
		return false
	}

	return true
}

// RemoveCDROMVolume removes the CDROM volume from a running VM using hotunplug.
// This is typically called after the OS has been installed from the ISO.
// The function patches the VM to remove the cdrom-iso disk and volume.
func (m *VMManager) RemoveCDROMVolume(ctx context.Context, machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) error {
	logger := log.FromContext(ctx)

	if vm == nil {
		return fmt.Errorf("VM is nil")
	}

	// Get fresh copy of VM
	currentVM := &kubevirtv1.VirtualMachine{}
	if err := m.remoteClient.Get(ctx, client.ObjectKeyFromObject(vm), currentVM); err != nil {
		return fmt.Errorf("failed to get current VM state: %w", err)
	}

	// Find and remove the CDROM disk and volume
	originalVM := currentVM.DeepCopy()

	// Remove CDROM disk
	newDisks := make([]kubevirtv1.Disk, 0, len(currentVM.Spec.Template.Spec.Domain.Devices.Disks))
	for i := range currentVM.Spec.Template.Spec.Domain.Devices.Disks {
		if currentVM.Spec.Template.Spec.Domain.Devices.Disks[i].Name != CDROMVolumeName {
			newDisks = append(newDisks, currentVM.Spec.Template.Spec.Domain.Devices.Disks[i])
		}
	}
	currentVM.Spec.Template.Spec.Domain.Devices.Disks = newDisks

	// Remove CDROM volume
	newVolumes := make([]kubevirtv1.Volume, 0, len(currentVM.Spec.Template.Spec.Volumes))
	for i := range currentVM.Spec.Template.Spec.Volumes {
		if currentVM.Spec.Template.Spec.Volumes[i].Name != CDROMVolumeName {
			newVolumes = append(newVolumes, currentVM.Spec.Template.Spec.Volumes[i])
		}
	}
	currentVM.Spec.Template.Spec.Volumes = newVolumes

	// Remove the DataVolume template for the ISO
	if len(currentVM.Spec.DataVolumeTemplates) > 0 {
		newDVTemplates := make([]kubevirtv1.DataVolumeTemplateSpec, 0, len(currentVM.Spec.DataVolumeTemplates))
		for i := range currentVM.Spec.DataVolumeTemplates {
			if currentVM.Spec.DataVolumeTemplates[i].Name != vm.Name+"-iso" {
				newDVTemplates = append(newDVTemplates, currentVM.Spec.DataVolumeTemplates[i])
			}
		}
		currentVM.Spec.DataVolumeTemplates = newDVTemplates
	}

	// Patch the VM
	if err := m.remoteClient.Patch(ctx, currentVM, client.MergeFrom(originalVM)); err != nil {
		return fmt.Errorf("failed to patch VM to remove CDROM: %w", err)
	}

	logger.Info("Successfully removed CDROM volume from VM", "vm", vm.Name)

	// Mark the CDROM as ejected in machine annotations
	if err := m.markCDROMEjected(ctx, machine); err != nil {
		logger.Error(err, "Failed to mark CDROM as ejected in machine annotations")
		// Don't return error - the CDROM was already removed from VM
	}

	return nil
}

// markCDROMEjected adds an annotation to the Machine indicating the CDROM was ejected
func (m *VMManager) markCDROMEjected(ctx context.Context, machine *vitistackv1alpha1.Machine) error {
	// Get fresh copy of machine
	currentMachine := &vitistackv1alpha1.Machine{}
	if err := m.supervisorClient.Get(ctx, client.ObjectKeyFromObject(machine), currentMachine); err != nil {
		return fmt.Errorf("failed to get current machine state: %w", err)
	}

	originalMachine := currentMachine.DeepCopy()

	if currentMachine.Annotations == nil {
		currentMachine.Annotations = make(map[string]string)
	}
	currentMachine.Annotations[AnnotationCDROMEjected] = AnnotationValueTrue

	if err := m.supervisorClient.Patch(ctx, currentMachine, client.MergeFrom(originalMachine)); err != nil {
		return fmt.Errorf("failed to patch machine annotations: %w", err)
	}

	return nil
}

// HasCDROMVolume checks if the VM has a CDROM volume attached
func HasCDROMVolume(vm *kubevirtv1.VirtualMachine) bool {
	if vm == nil || vm.Spec.Template == nil {
		return false
	}

	for i := range vm.Spec.Template.Spec.Volumes {
		if vm.Spec.Template.Spec.Volumes[i].Name == CDROMVolumeName {
			return true
		}
	}
	return false
}

// IsCDROMEjected checks if the CDROM has already been ejected from the machine
func IsCDROMEjected(machine *vitistackv1alpha1.Machine) bool {
	if machine.Annotations == nil {
		return false
	}
	return machine.Annotations[AnnotationCDROMEjected] == AnnotationValueTrue
}

// WasCreatedWithISO checks if the machine was created with an ISO boot source
func WasCreatedWithISO(machine *vitistackv1alpha1.Machine) bool {
	return machine.Spec.OS.ImageID != ""
}
