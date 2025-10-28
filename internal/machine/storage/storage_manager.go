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

package storage

import (
	"context"
	"fmt"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/crds/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// StorageManager handles storage-related operations for machines
type StorageManager struct {
	client.Client
	Scheme *runtime.Scheme
}

// NewManager creates a new storage manager
func NewManager(c client.Client, scheme *runtime.Scheme) *StorageManager {
	return &StorageManager{
		Client: c,
		Scheme: scheme,
	}
}

// GetDefaultStorageClass finds and returns the default storage class name from the specified cluster
func (m *StorageManager) GetDefaultStorageClass(ctx context.Context, clusterClient client.Client) (string, error) {
	logger := log.FromContext(ctx)

	// List all storage classes from the specified cluster (remote KubeVirt cluster)
	storageClasses := &storagev1.StorageClassList{}
	if err := clusterClient.List(ctx, storageClasses); err != nil {
		logger.Error(err, "Failed to list storage classes")
		return "", err
	}

	// Look for the default storage class
	for i := range storageClasses.Items { // index loop avoids copying struct
		sc := &storageClasses.Items[i]
		if sc.Annotations != nil {
			if isDefault, exists := sc.Annotations["storageclass.kubernetes.io/is-default-class"]; exists && isDefault == "true" {
				logger.Info("Found default storage class", "storageClass", sc.Name)
				return sc.Name, nil
			}
		}
	}

	// No default storage class found
	return "", fmt.Errorf("no default storage class found in the cluster")
}

// CreatePVCsFromDiskSpecs creates PVCs for each disk in machine.spec.disks on the remote cluster
func (m *StorageManager) CreatePVCsFromDiskSpecs(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string, remoteClient client.Client) ([]string, error) {
	logger := log.FromContext(ctx)

	// Get default storage class from the remote KubeVirt cluster
	defaultStorageClass, err := m.GetDefaultStorageClass(ctx, remoteClient)
	if err != nil {
		return nil, fmt.Errorf("failed to get default storage class: %w", err)
	}

	pvcNames := make([]string, 0, max(1, len(machine.Spec.Disks)))

	// If no disks are specified in the spec, create a default root disk
	if len(machine.Spec.Disks) == 0 {
		pvcName := fmt.Sprintf("%s-root-pvc", vmName)
		if err := m.createSinglePVC(ctx, machine, pvcName, "10Gi", defaultStorageClass, true, remoteClient); err != nil {
			return nil, fmt.Errorf("failed to create default root PVC: %w", err)
		}
		pvcNames = append(pvcNames, pvcName)
		return pvcNames, nil
	}

	// Create PVCs for each disk in the spec
	for i := range machine.Spec.Disks {
		disk := &machine.Spec.Disks[i]
		var pvcName string
		if disk.Name != "" {
			pvcName = fmt.Sprintf("%s-%s-pvc", vmName, disk.Name)
		} else {
			pvcName = fmt.Sprintf("%s-disk%d-pvc", vmName, i)
		}

		// Convert sizeGB to storage size string
		storageSize := fmt.Sprintf("%dGi", disk.SizeGB)

		if err := m.createSinglePVC(ctx, machine, pvcName, storageSize, defaultStorageClass, disk.Boot, remoteClient); err != nil {
			return nil, fmt.Errorf("failed to create PVC %s: %w", pvcName, err)
		}

		pvcNames = append(pvcNames, pvcName)
		logger.Info("Created PVC for disk", "pvc", pvcName, "size", storageSize, "boot", disk.Boot)
	}

	return pvcNames, nil
}

// createSinglePVC creates a single PVC with the specified parameters on the remote cluster
func (m *StorageManager) createSinglePVC(ctx context.Context, machine *vitistackv1alpha1.Machine, pvcName, storageSize, storageClass string, isBoot bool, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	// Check if PVC already exists on the remote cluster
	existingPVC := &corev1.PersistentVolumeClaim{}
	err := remoteClient.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: machine.Namespace}, existingPVC)
	if err == nil {
		// PVC already exists, skip creation
		logger.V(1).Info("PVC already exists, skipping creation", "pvc", pvcName)
		return nil
	}
	if !errors.IsNotFound(err) {
		// Some other error occurred
		return err
	}

	labels := map[string]string{
		"managed-by":     viper.GetString(consts.MANAGED_BY),
		"source-machine": machine.Name,
	}

	if isBoot {
		labels["disk-type"] = "boot"
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: machine.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{
				corev1.ReadWriteOnce,
			},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(storageSize),
				},
			},
			StorageClassName: &storageClass,
		},
	}

	// Note: We do NOT set Machine as the owner reference for the PVC because
	// they exist in different clusters (Machine on supervisor, PVC on remote KubeVirt cluster).
	// Cross-cluster owner references are not supported in Kubernetes.
	// Instead, we rely on labels (source-machine, managed-by) for tracking and cleanup.

	// Create PVC on the remote KubeVirt cluster
	if err := remoteClient.Create(ctx, pvc); err != nil {
		return err
	}

	logger.Info("Successfully created PVC", "pvc", pvc.Name, "size", storageSize, "storageClass", storageClass)
	return nil
}

// DeleteAssociatedPVCs deletes all PVCs associated with a machine from the remote cluster
func (m *StorageManager) DeleteAssociatedPVCs(ctx context.Context, machine *vitistackv1alpha1.Machine, vmName string, remoteClient client.Client) error {
	logger := log.FromContext(ctx)

	// List all PVCs in the namespace with the machine's label from the remote cluster
	pvcList := &corev1.PersistentVolumeClaimList{}
	listOpts := []client.ListOption{
		client.InNamespace(machine.Namespace),
		client.MatchingLabels{
			"managed-by":     viper.GetString(consts.MANAGED_BY),
			"source-machine": machine.Name,
		},
	}

	if err := remoteClient.List(ctx, pvcList, listOpts...); err != nil {
		logger.Error(err, "Failed to list PVCs for deletion")
		return err
	}

	// Delete each PVC (index loop to avoid copying large structs each iteration)
	for i := range pvcList.Items {
		pvc := &pvcList.Items[i]
		if err := remoteClient.Delete(ctx, pvc); err != nil {
			logger.Error(err, "Failed to delete PVC", "pvc", pvc.Name)
			return err
		}
		logger.Info("Deleted PVC", "pvc", pvc.Name)
	}

	// Also try to delete any legacy PVCs with the old naming convention
	legacyPVCName := fmt.Sprintf("%s-pvc", vmName)
	legacyPVC := &corev1.PersistentVolumeClaim{}
	legacyPVCNamespacedName := types.NamespacedName{
		Name:      legacyPVCName,
		Namespace: machine.Namespace,
	}

	err := remoteClient.Get(ctx, legacyPVCNamespacedName, legacyPVC)
	if err == nil {
		// Legacy PVC exists, delete it
		if err := remoteClient.Delete(ctx, legacyPVC); err != nil {
			logger.Error(err, "Failed to delete legacy PVC")
			return err
		}
		logger.Info("Deleted legacy PVC", "pvc", legacyPVC.Name)
	} else if !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get legacy PVC for deletion")
		return err
	}

	return nil
}
