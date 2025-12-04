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
	"fmt"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

// Bus type constants for disk configuration
const (
	BusTypeVirtio kubevirtv1.DiskBus = "virtio"
	BusTypeSata   kubevirtv1.DiskBus = "sata"
	BusTypeScsi   kubevirtv1.DiskBus = "scsi"
)

// buildDisksAndVolumes creates disk and volume specifications for the VM
func (m *VMManager) buildDisksAndVolumes(machine *vitistackv1alpha1.Machine, pvcNames []string) ([]kubevirtv1.Disk, []kubevirtv1.Volume) {
	l := len(machine.Spec.Disks)
	// Always allocate with capacity (>=1 for default root case)
	capSize := l
	if capSize == 0 {
		capSize = 1
	}
	disks := make([]kubevirtv1.Disk, 0, capSize)
	volumes := make([]kubevirtv1.Volume, 0, capSize)

	bootorder := uint(1) // Default boot order for the first disk

	// If no disks are specified in the spec, create a default root disk
	if len(machine.Spec.Disks) == 0 {
		disks = append(disks, kubevirtv1.Disk{
			Name: "root",
			DiskDevice: kubevirtv1.DiskDevice{
				Disk: &kubevirtv1.DiskTarget{
					Bus: BusTypeVirtio,
				},
			},
			BootOrder: &bootorder,
		})

		volumes = append(volumes, kubevirtv1.Volume{
			Name: "root",
			VolumeSource: kubevirtv1.VolumeSource{
				PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{
					PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcNames[0],
					},
				},
			},
		})

		return disks, volumes
	}

	// Create disks and volumes based on machine.spec.disks
	for i, diskSpec := range machine.Spec.Disks {
		diskName := "root"
		if diskSpec.Name != "" {
			diskName = diskSpec.Name
		} else if i > 0 {
			diskName = fmt.Sprintf("disk%d", i)
		}

		// Determine bus type (virtio is generally preferred for performance)
		busType := determineBusType(diskSpec.Type)

		disk := kubevirtv1.Disk{
			Name: diskName,
			DiskDevice: kubevirtv1.DiskDevice{
				Disk: &kubevirtv1.DiskTarget{
					Bus: busType,
				},
			},
		}

		// Set boot order for boot disk
		if diskSpec.Boot {
			bootOrder := uint(1)
			disk.BootOrder = &bootOrder
		}

		disks = append(disks, disk)

		volume := kubevirtv1.Volume{
			Name: diskName,
			VolumeSource: kubevirtv1.VolumeSource{
				PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{
					PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcNames[i],
					},
				},
			},
		}

		volumes = append(volumes, volume)
	}

	return disks, volumes
}

// determineBusType maps disk types to appropriate KubeVirt bus types
func determineBusType(diskType string) kubevirtv1.DiskBus {
	if diskType == "" {
		return BusTypeVirtio
	}

	switch diskType {
	case "nvme":
		return BusTypeVirtio // KubeVirt uses virtio for NVMe-style performance
	case "sata":
		return BusTypeSata
	case "scsi":
		return BusTypeScsi
	default:
		return BusTypeVirtio
	}
}

// addISOBootSource adds an ISO image as a CDROM boot source
func (m *VMManager) addISOBootSource(disks []kubevirtv1.Disk, volumes []kubevirtv1.Volume, vmName string) ([]kubevirtv1.Disk, []kubevirtv1.Volume) {
	// Add CDROM disk for ISO with boot order 2 (after root disk)
	// This allows booting from ISO for installation, but prioritizes
	// the root disk after OS installation completes
	bootOrder := uint(2)
	disks = append(disks, kubevirtv1.Disk{
		Name: CDROMVolumeName,
		DiskDevice: kubevirtv1.DiskDevice{
			CDRom: &kubevirtv1.CDRomTarget{
				Bus: BusTypeSata,
			},
		},
		BootOrder: &bootOrder,
	})

	// Add volume referencing the DataVolume
	volumes = append(volumes, kubevirtv1.Volume{
		Name: CDROMVolumeName,
		VolumeSource: kubevirtv1.VolumeSource{
			DataVolume: &kubevirtv1.DataVolumeSource{
				Name: vmName + "-iso",
			},
		},
	})

	// Root disk keeps boot order 1, ISO is boot order 2
	// Most firmware will skip empty disks and boot from ISO on first run
	// After installation, it will boot from the installed OS on root disk

	return disks, volumes
}

// buildDataVolumeTemplates creates DataVolume templates for ISO boot
func (m *VMManager) buildDataVolumeTemplates(machine *vitistackv1alpha1.Machine, vmName string) []kubevirtv1.DataVolumeTemplateSpec {
	bootSourceType := machine.Annotations[AnnotationBootSource]
	if bootSourceType != BootSourceDataVolume || machine.Spec.OS.ImageID == "" {
		return nil
	}

	// Determine source type (http, registry, pvc, etc.)
	sourceType := machine.Annotations["kubevirt.io/boot-source-type"]
	if sourceType == "" {
		sourceType = "http" // Default to HTTP
	}

	storageSize := resource.MustParse("10Gi")
	// ISO images require Filesystem volume mode for CDROM
	filesystemMode := corev1.PersistentVolumeFilesystem

	template := kubevirtv1.DataVolumeTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name: vmName + "-iso",
			Labels: map[string]string{
				vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
				"vitistack.io/source-machine":         machine.Name,
			},
		},
		Spec: cdiv1.DataVolumeSpec{
			Storage: &cdiv1.StorageSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
				VolumeMode: &filesystemMode,
			},
		},
	}

	// Build the DataVolume source based on type
	switch sourceType {
	case "http", "https":
		template.Spec.Source = &cdiv1.DataVolumeSource{
			HTTP: &cdiv1.DataVolumeSourceHTTP{
				URL: machine.Spec.OS.ImageID,
			},
		}
	case "registry":
		template.Spec.Source = &cdiv1.DataVolumeSource{
			Registry: &cdiv1.DataVolumeSourceRegistry{
				URL: &machine.Spec.OS.ImageID,
			},
		}
	}

	return []kubevirtv1.DataVolumeTemplateSpec{template}
}
