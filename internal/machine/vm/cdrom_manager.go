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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// CDROM volume management constants
const (
	// CDROMVolumeName is the name of the CDROM volume used for ISO boot
	CDROMVolumeName = "cdrom-iso"

	// AnnotationCDROMEjected indicates the ISO resources should be cleaned up.
	// This is set by the user or an external operator after OS installation.
	// When set to "true", the ISO DataVolume/PVC will be deleted to free storage.
	// Note: Boot order ensures the installed OS on root disk boots first,
	// so removal is purely for resource cleanup.
	AnnotationCDROMEjected = "vitistack.io/cdrom-ejected"

	// AnnotationOSInstalled indicates the OS has been installed to disk.
	// This is typically set by an external operator (e.g., talos-operator)
	// after the OS installation completes and the system is ready to boot from disk.
	// When set to "true", the ISO DataVolume/PVC will be deleted to free storage.
	AnnotationOSInstalled = "vitistack.io/os-installed"

	// AnnotationBootSource indicates the boot source type (from Machine spec)
	AnnotationBootSource = "kubevirt.io/boot-source"

	// BootSourceDataVolume indicates the VM boots from a DataVolume (ISO)
	BootSourceDataVolume = "datavolume"

	// AnnotationValueTrue is the string value for true annotations
	AnnotationValueTrue = "true"

	// AnnotationISOCleanedUp marks that ISO resources have already been cleaned up
	AnnotationISOCleanedUp = "vitistack.io/iso-cleaned-up"
)

// ISOResourceName returns the name shared by the boot ISO's DataVolume, its
// backing PVC, and the matching DataVolumeTemplates entry on the VM. Keep all
// callers routed through this helper so the naming rule lives in one place.
func ISOResourceName(vmName string) string {
	return vmName + "-iso"
}

// ShouldCleanupISO determines if the ISO resources should be cleaned up.
// The ISO DataVolume/PVC should be deleted when:
//  1. Either AnnotationCDROMEjected or AnnotationOSInstalled is set to "true".
//  2. AND any of the following is true:
//     a. The ISO hasn't been cleaned up yet (AnnotationISOCleanedUp != "true").
//     b. AnnotationISOCleanedUp is set but the VM spec still references the
//     CDROM volume — meaning a previous cleanup pass deleted DV/PVC without
//     stripping the spec, leaving the PVC stuck in Terminating. Re-run the
//     cleanup so the spec gets patched and storage is actually released.
//
// Note: This is purely for resource cleanup. Boot order ensures the root disk
// (with installed OS) boots before the CDROM, so removal doesn't affect boot.
func (m *VMManager) ShouldCleanupISO(machine *vitistackv1alpha1.Machine, vm *kubevirtv1.VirtualMachine) bool {
	if !IsMarkedForCDROMRemoval(machine) {
		return false
	}

	if machine.Annotations[AnnotationISOCleanedUp] != AnnotationValueTrue {
		return true
	}

	// Dedup says cleanup ran, but verify the spec is actually clean. If it
	// still references the CDROM, the previous pass was incomplete (e.g. ran
	// before this operator learned to patch the VM spec) and we must resume.
	return HasCDROMVolume(vm)
}

// CleanupISOResources deletes the ISO DataVolume and PVC to free storage,
// and strips the cdrom-iso Disk/Volume/DataVolumeTemplate references from
// the VM spec so a future VMI restart doesn't fail trying to mount a now-
// deleted volume. The VM-spec patch is non-disruptive: KubeVirt accepts it
// on a running VM and applies it on the next VMI restart.
func (m *VMManager) CleanupISOResources(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string) error {
	logger := log.FromContext(ctx)

	// Strip CDROM references from the VM spec first. Without this, the running
	// virt-launcher pod keeps holding the PVC via kubernetes.io/pvc-protection
	// (so the PVC sits in Terminating forever), and any future VMI restart
	// would fail with a missing-volume error.
	if err := m.removeCDROMFromVMSpec(ctx, machine, vmName); err != nil {
		return fmt.Errorf("failed to remove CDROM from VM spec: %w", err)
	}

	isoName := ISOResourceName(vmName)
	namespace := machine.Namespace

	// Delete the DataVolume (this should cascade delete the PVC)
	dv := &cdiv1.DataVolume{}
	dvKey := types.NamespacedName{Name: isoName, Namespace: namespace}
	if err := m.remoteClient.Get(ctx, dvKey, dv); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("ISO DataVolume already deleted or doesn't exist", "name", isoName)
		} else {
			return fmt.Errorf("failed to get ISO DataVolume: %w", err)
		}
	} else {
		if err := m.remoteClient.Delete(ctx, dv); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete ISO DataVolume: %w", err)
		}
		logger.Info("Deleted ISO DataVolume", "name", isoName)
	}

	// Also try to delete the PVC directly in case it wasn't cascade deleted
	pvc := &corev1.PersistentVolumeClaim{}
	pvcKey := types.NamespacedName{Name: isoName, Namespace: namespace}
	if err := m.remoteClient.Get(ctx, pvcKey, pvc); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("ISO PVC already deleted or doesn't exist", "name", isoName)
		} else {
			return fmt.Errorf("failed to get ISO PVC: %w", err)
		}
	} else {
		if err := m.remoteClient.Delete(ctx, pvc); err != nil && !errors.IsNotFound(err) {
			return fmt.Errorf("failed to delete ISO PVC: %w", err)
		}
		logger.Info("Deleted ISO PVC", "name", isoName)
	}

	logger.Info("Successfully cleaned up ISO resources",
		"vm", vmName,
		"reason", getCDROMRemovalReason(machine))

	return nil
}

// removeCDROMFromVMSpec strips the cdrom-iso Disk, Volume, and matching
// DataVolumeTemplate from the VM spec via a MergeFrom patch. Idempotent:
// returns nil with no patch if the references are already absent or the VM
// no longer exists.
func (m *VMManager) removeCDROMFromVMSpec(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string) error {
	logger := log.FromContext(ctx)

	vm := &kubevirtv1.VirtualMachine{}
	vmKey := types.NamespacedName{Name: vmName, Namespace: machine.Namespace}
	if err := m.remoteClient.Get(ctx, vmKey, vm); err != nil {
		if errors.IsNotFound(err) {
			logger.V(1).Info("VM not found, skipping CDROM removal", "name", vmName)
			return nil
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	original := vm.DeepCopy()
	dvName := ISOResourceName(vmName)
	changed := false

	if vm.Spec.Template != nil {
		disks := vm.Spec.Template.Spec.Domain.Devices.Disks
		filteredDisks := make([]kubevirtv1.Disk, 0, len(disks))
		for i := range disks {
			if disks[i].Name == CDROMVolumeName {
				changed = true
				continue
			}
			filteredDisks = append(filteredDisks, disks[i])
		}
		vm.Spec.Template.Spec.Domain.Devices.Disks = filteredDisks

		volumes := vm.Spec.Template.Spec.Volumes
		filteredVolumes := make([]kubevirtv1.Volume, 0, len(volumes))
		for i := range volumes {
			if volumes[i].Name == CDROMVolumeName {
				changed = true
				continue
			}
			filteredVolumes = append(filteredVolumes, volumes[i])
		}
		vm.Spec.Template.Spec.Volumes = filteredVolumes
	}

	dvts := vm.Spec.DataVolumeTemplates
	filteredDVTs := make([]kubevirtv1.DataVolumeTemplateSpec, 0, len(dvts))
	for i := range dvts {
		if dvts[i].Name == dvName {
			changed = true
			continue
		}
		filteredDVTs = append(filteredDVTs, dvts[i])
	}
	vm.Spec.DataVolumeTemplates = filteredDVTs

	if !changed {
		return nil
	}

	patch := client.MergeFrom(original)
	if err := m.remoteClient.Patch(ctx, vm, patch); err != nil {
		return fmt.Errorf("failed to patch VM to remove CDROM references: %w", err)
	}
	logger.Info("Removed CDROM references from VM spec", "vm", vmName)
	return nil
}

// getCDROMRemovalReason returns a human-readable reason for CDROM removal
func getCDROMRemovalReason(machine *vitistackv1alpha1.Machine) string {
	if machine.Annotations == nil {
		return "unknown"
	}
	if machine.Annotations[AnnotationOSInstalled] == AnnotationValueTrue {
		return "os-installed"
	}
	if machine.Annotations[AnnotationCDROMEjected] == AnnotationValueTrue {
		return "cdrom-ejected"
	}
	return "unknown"
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

// IsMarkedForCDROMRemoval checks if the machine has annotations indicating
// the ISO resources should be cleaned up. Returns true if either:
// - AnnotationCDROMEjected is set to "true"
// - AnnotationOSInstalled is set to "true"
func IsMarkedForCDROMRemoval(machine *vitistackv1alpha1.Machine) bool {
	if machine == nil || machine.Annotations == nil {
		return false
	}

	// Check if cdrom-ejected annotation is set
	if machine.Annotations[AnnotationCDROMEjected] == AnnotationValueTrue {
		return true
	}

	// Check if os-installed annotation is set (Talos scenario)
	if machine.Annotations[AnnotationOSInstalled] == AnnotationValueTrue {
		return true
	}

	return false
}

// IsCDROMEjected checks if the machine has the cdrom-ejected annotation set
func IsCDROMEjected(machine *vitistackv1alpha1.Machine) bool {
	if machine == nil || machine.Annotations == nil {
		return false
	}
	return machine.Annotations[AnnotationCDROMEjected] == AnnotationValueTrue
}

// IsOSInstalled checks if the machine has the os-installed annotation set
func IsOSInstalled(machine *vitistackv1alpha1.Machine) bool {
	if machine == nil || machine.Annotations == nil {
		return false
	}
	return machine.Annotations[AnnotationOSInstalled] == AnnotationValueTrue
}

// WasCreatedWithISO checks if the machine was created with an ISO boot source
func WasCreatedWithISO(machine *vitistackv1alpha1.Machine) bool {
	return machine != nil && machine.Spec.OS.ImageID != ""
}
