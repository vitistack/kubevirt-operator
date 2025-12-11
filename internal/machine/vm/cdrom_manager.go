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

// ShouldCleanupISO determines if the ISO resources should be cleaned up.
// The ISO DataVolume/PVC should be deleted when:
// 1. Either AnnotationCDROMEjected or AnnotationOSInstalled is set to "true"
// 2. The ISO hasn't already been cleaned up (to avoid repeated attempts)
//
// Note: This is purely for resource cleanup. Boot order ensures the root disk
// (with installed OS) boots before the CDROM, so removal doesn't affect boot.
func (m *VMManager) ShouldCleanupISO(machine *vitistackv1alpha1.Machine) bool {
	// Check if cleanup is requested via annotations
	if !IsMarkedForCDROMRemoval(machine) {
		return false
	}

	// Check if already cleaned up
	if machine.Annotations[AnnotationISOCleanedUp] == AnnotationValueTrue {
		return false
	}

	return true
}

// CleanupISOResources deletes the ISO DataVolume and PVC to free storage.
// This is called when the machine is marked for CDROM removal via annotations.
// The VM doesn't need to be modified - boot order ensures root disk boots first.
func (m *VMManager) CleanupISOResources(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string) error {
	logger := log.FromContext(ctx)

	isoName := vmName + "-iso"
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
