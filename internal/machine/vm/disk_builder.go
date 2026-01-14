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
	"net/http"
	"time"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Bus type constants for disk configuration
const (
	BusTypeVirtio kubevirtv1.DiskBus = "virtio"
	BusTypeSata   kubevirtv1.DiskBus = "sata"
	BusTypeScsi   kubevirtv1.DiskBus = "scsi"

	// SourceTypeHTTP is the default source type for ISO boot
	SourceTypeHTTP = "http"
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
	isARM64 := isARM64Architecture(machine)

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
		// ARM64 only supports virtio and scsi bus types
		busType := determineBusType(diskSpec.Type, isARM64)

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
// ARM64 only supports virtio and scsi bus types (no SATA support)
// See: https://kubevirt.io/user-guide/cluster_admin/virtual_machines_on_Arm64/
func determineBusType(diskType string, isARM64 bool) kubevirtv1.DiskBus {
	if diskType == "" {
		return BusTypeVirtio
	}

	switch diskType {
	case "nvme":
		return BusTypeVirtio // KubeVirt uses virtio for NVMe-style performance
	case "sata":
		// ARM64 does not support SATA, fall back to virtio
		if isARM64 {
			return BusTypeVirtio
		}
		return BusTypeSata
	case "scsi":
		return BusTypeScsi
	default:
		return BusTypeVirtio
	}
}

// addISOBootSource adds an ISO image as a CDROM boot source
// Note: ARM64 does not support SATA bus for CDROM, so we use SCSI instead
// See: https://kubevirt.io/user-guide/cluster_admin/virtual_machines_on_Arm64/
func (m *VMManager) addISOBootSource(disks []kubevirtv1.Disk, volumes []kubevirtv1.Volume, vmName string, machine *vitistackv1alpha1.Machine) ([]kubevirtv1.Disk, []kubevirtv1.Volume) {
	// Add CDROM disk for ISO with boot order 2 (after root disk)
	// This allows booting from ISO for installation, but prioritizes
	// the root disk after OS installation completes
	bootOrder := uint(2)

	// Determine CDROM bus type - ARM64 only supports SCSI, not SATA
	cdromBus := BusTypeSata
	if isARM64Architecture(machine) {
		cdromBus = BusTypeScsi
	}

	disks = append(disks, kubevirtv1.Disk{
		Name: CDROMVolumeName,
		DiskDevice: kubevirtv1.DiskDevice{
			CDRom: &kubevirtv1.CDRomTarget{
				Bus: cdromBus,
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
func (m *VMManager) buildDataVolumeTemplates(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string) []kubevirtv1.DataVolumeTemplateSpec {
	logger := log.FromContext(ctx)

	bootSourceType := machine.Annotations[AnnotationBootSource]
	if bootSourceType != BootSourceDataVolume || machine.Spec.OS.ImageID == "" {
		return nil
	}

	// Determine source type (http, registry, pvc, etc.)
	sourceType := machine.Annotations["kubevirt.io/boot-source-type"]
	if sourceType == "" {
		sourceType = SourceTypeHTTP // Default to HTTP
	}

	// Try to get the actual ISO size, fall back to default 8Gi
	storageSize := getISOStorageSize(ctx, machine.Spec.OS.ImageID, sourceType)
	logger.V(1).Info("Determined ISO storage size", "url", machine.Spec.OS.ImageID, "size", storageSize.String())

	// ISO images require Filesystem volume mode for CDROM
	filesystemMode := corev1.PersistentVolumeFilesystem

	// Get access mode from config (default: ReadWriteOnce)
	accessMode := corev1.PersistentVolumeAccessMode(viper.GetString(consts.PVC_ACCESS_MODE))
	if accessMode == "" {
		accessMode = corev1.ReadWriteOnce
	}

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
				AccessModes: []corev1.PersistentVolumeAccessMode{
					accessMode,
				},
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

// DefaultISOStorageSize is the fallback size when ISO size cannot be determined
const DefaultISOStorageSize = "8Gi"

// ISOSizeHTTPTimeout is the timeout for HTTP HEAD requests to get ISO size
const ISOSizeHTTPTimeout = 10 * time.Second

// getISOStorageSize attempts to determine the storage size needed for an ISO.
// For HTTP/HTTPS sources, it makes a HEAD request to get Content-Length.
// Returns the actual size rounded up to the nearest Gi, or DefaultISOStorageSize on failure.
func getISOStorageSize(ctx context.Context, imageURL string, sourceType string) resource.Quantity {
	logger := log.FromContext(ctx)

	// Only HTTP/HTTPS sources support HEAD requests
	if sourceType != SourceTypeHTTP && sourceType != "https" {
		return resource.MustParse(DefaultISOStorageSize)
	}

	// Create HTTP client with timeout that follows redirects
	client := &http.Client{
		Timeout: ISOSizeHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Allow up to 10 redirects
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	// Make HEAD request to get Content-Length without downloading
	resp, err := client.Head(imageURL)
	if err != nil {
		logger.V(1).Info("Failed to get ISO size via HEAD request, using default",
			"url", imageURL, "error", err.Error(), "default", DefaultISOStorageSize)
		return resource.MustParse(DefaultISOStorageSize)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Check if we got a successful response
	if resp.StatusCode != http.StatusOK {
		logger.V(1).Info("HEAD request returned non-OK status, using default size",
			"url", imageURL, "status", resp.StatusCode, "default", DefaultISOStorageSize)
		return resource.MustParse(DefaultISOStorageSize)
	}

	// Get Content-Length header
	contentLength := resp.ContentLength
	if contentLength <= 0 {
		logger.V(1).Info("Content-Length not available, using default size",
			"url", imageURL, "default", DefaultISOStorageSize)
		return resource.MustParse(DefaultISOStorageSize)
	}

	// Convert bytes to Gi (1 Gi = 1024^3 bytes)
	const bytesPerGi = 1024 * 1024 * 1024

	// Add 10% buffer for filesystem overhead, then round up to nearest Gi
	sizeWithBuffer := int64(float64(contentLength) * 1.1)
	sizeInGi := (sizeWithBuffer + bytesPerGi - 1) / bytesPerGi // Ceiling division

	// Minimum 1Gi
	if sizeInGi < 1 {
		sizeInGi = 1
	}

	sizeStr := fmt.Sprintf("%dGi", sizeInGi)
	logger.Info("Determined ISO size from Content-Length",
		"url", imageURL, "contentLengthBytes", contentLength, "allocatedSize", sizeStr)

	return resource.MustParse(sizeStr)
}
