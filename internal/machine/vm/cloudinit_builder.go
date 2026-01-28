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
	"encoding/base64"
	"fmt"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// CloudInitVolumeName is the name of the cloud-init volume
	CloudInitVolumeName = "cloudinit"

	// DefaultUserDataKey is the default key for user data in ConfigMaps/Secrets
	DefaultUserDataKey = "userdata"

	// DefaultNetworkDataKey is the default key for network data in ConfigMaps/Secrets
	DefaultNetworkDataKey = "networkdata"
)

// CloudInitData holds the resolved cloud-init data
type CloudInitData struct {
	UserData    string
	NetworkData string
	Type        vitistackv1alpha1.CloudInitType
}

// buildCloudInitVolume creates cloud-init disk and volume for the VM
// Returns nil, nil if cloud-init is not configured
func (m *VMManager) buildCloudInitVolume(ctx context.Context, machine *vitistackv1alpha1.Machine) (*kubevirtv1.Disk, *kubevirtv1.Volume, error) {
	logger := log.FromContext(ctx)

	if machine.Spec.CloudInit == nil {
		logger.V(1).Info("No cloud-init configuration specified")
		return nil, nil, nil
	}

	cloudInitConfig := machine.Spec.CloudInit

	// Resolve cloud-init data from various sources
	cloudInitData, err := m.resolveCloudInitData(ctx, machine)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve cloud-init data: %w", err)
	}

	// If no data was resolved, skip cloud-init
	if cloudInitData.UserData == "" && cloudInitData.NetworkData == "" {
		logger.V(1).Info("No cloud-init data resolved, skipping cloud-init volume")
		return nil, nil, nil
	}

	// Create the cloud-init disk
	disk := &kubevirtv1.Disk{
		Name: CloudInitVolumeName,
		DiskDevice: kubevirtv1.DiskDevice{
			Disk: &kubevirtv1.DiskTarget{
				Bus: BusTypeVirtio,
			},
		},
	}

	// Create the volume based on cloud-init type
	volume := &kubevirtv1.Volume{
		Name: CloudInitVolumeName,
	}

	cloudInitType := cloudInitConfig.Type
	if cloudInitType == "" {
		cloudInitType = vitistackv1alpha1.CloudInitTypeNoCloud
	}

	switch cloudInitType {
	case vitistackv1alpha1.CloudInitTypeNoCloud:
		volume.VolumeSource = kubevirtv1.VolumeSource{
			CloudInitNoCloud: &kubevirtv1.CloudInitNoCloudSource{
				UserData:    cloudInitData.UserData,
				NetworkData: cloudInitData.NetworkData,
			},
		}
		logger.V(1).Info("Created CloudInitNoCloud volume",
			"hasUserData", cloudInitData.UserData != "",
			"hasNetworkData", cloudInitData.NetworkData != "")

	case vitistackv1alpha1.CloudInitTypeConfigDrive:
		volume.VolumeSource = kubevirtv1.VolumeSource{
			CloudInitConfigDrive: &kubevirtv1.CloudInitConfigDriveSource{
				UserData:    cloudInitData.UserData,
				NetworkData: cloudInitData.NetworkData,
			},
		}
		logger.V(1).Info("Created CloudInitConfigDrive volume",
			"hasUserData", cloudInitData.UserData != "",
			"hasNetworkData", cloudInitData.NetworkData != "")

	default:
		return nil, nil, fmt.Errorf("unsupported cloud-init type: %s", cloudInitType)
	}

	return disk, volume, nil
}

// resolveCloudInitData resolves cloud-init data from all configured sources
// Priority: inline data > base64 data > secret ref > configmap ref
func (m *VMManager) resolveCloudInitData(ctx context.Context, machine *vitistackv1alpha1.Machine) (*CloudInitData, error) {
	logger := log.FromContext(ctx)
	cloudInitConfig := machine.Spec.CloudInit

	data := &CloudInitData{
		Type: cloudInitConfig.Type,
	}

	// Resolve user data
	userData, err := m.resolveUserData(ctx, machine)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve user data: %w", err)
	}
	data.UserData = userData

	// Resolve network data
	networkData, err := m.resolveNetworkData(ctx, machine)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve network data: %w", err)
	}
	data.NetworkData = networkData

	logger.V(1).Info("Resolved cloud-init data",
		"userDataLength", len(data.UserData),
		"networkDataLength", len(data.NetworkData))

	return data, nil
}

// resolveUserData resolves user data from the configured source
// Priority: inline > base64 > secret > configmap
func (m *VMManager) resolveUserData(ctx context.Context, machine *vitistackv1alpha1.Machine) (string, error) {
	cloudInitConfig := machine.Spec.CloudInit
	return m.resolveCloudInitDataFromSources(ctx, machine.Namespace,
		cloudInitConfig.UserData,
		cloudInitConfig.UserDataBase64,
		cloudInitConfig.UserDataSecretRef,
		cloudInitConfig.UserDataConfigMapRef,
		DefaultUserDataKey,
		"user data")
}

// resolveNetworkData resolves network data from the configured source
// Priority: inline > base64 > secret > configmap
func (m *VMManager) resolveNetworkData(ctx context.Context, machine *vitistackv1alpha1.Machine) (string, error) {
	cloudInitConfig := machine.Spec.CloudInit
	return m.resolveCloudInitDataFromSources(ctx, machine.Namespace,
		cloudInitConfig.NetworkData,
		cloudInitConfig.NetworkDataBase64,
		cloudInitConfig.NetworkDataSecretRef,
		cloudInitConfig.NetworkDataConfigMapRef,
		DefaultNetworkDataKey,
		"network data")
}

// resolveCloudInitDataFromSources is a generic helper that resolves cloud-init data from various sources
// Priority: inline > base64 > secret > configmap
func (m *VMManager) resolveCloudInitDataFromSources(
	ctx context.Context,
	namespace string,
	inlineData string,
	base64Data string,
	secretRef *vitistackv1alpha1.CloudInitSecretRef,
	configMapRef *vitistackv1alpha1.CloudInitConfigMapRef,
	defaultKey string,
	dataType string,
) (string, error) {
	// 1. Check inline data
	if inlineData != "" {
		return inlineData, nil
	}

	// 2. Check base64-encoded data
	if base64Data != "" {
		decoded, err := base64.StdEncoding.DecodeString(base64Data)
		if err != nil {
			return "", fmt.Errorf("failed to decode %s base64: %w", dataType, err)
		}
		return string(decoded), nil
	}

	// 3. Check secret reference
	if secretRef != nil {
		data, err := m.getDataFromSecret(ctx, namespace, secretRef.Name,
			getKeyOrDefault(secretRef.Key, defaultKey))
		if err != nil {
			return "", fmt.Errorf("failed to get %s from secret: %w", dataType, err)
		}
		return data, nil
	}

	// 4. Check configmap reference
	if configMapRef != nil {
		data, err := m.getDataFromConfigMap(ctx, namespace, configMapRef.Name,
			getKeyOrDefault(configMapRef.Key, defaultKey))
		if err != nil {
			return "", fmt.Errorf("failed to get %s from configmap: %w", dataType, err)
		}
		return data, nil
	}

	return "", nil
}

// getDataFromSecret retrieves data from a Secret
func (m *VMManager) getDataFromSecret(ctx context.Context, namespace, name, key string) (string, error) {
	logger := log.FromContext(ctx)

	secret := &corev1.Secret{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, secret); err != nil {
		return "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
	}

	data, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}

	logger.V(1).Info("Retrieved cloud-init data from secret",
		"secret", name, "key", key, "dataLength", len(data))

	return string(data), nil
}

// getDataFromConfigMap retrieves data from a ConfigMap
func (m *VMManager) getDataFromConfigMap(ctx context.Context, namespace, name, key string) (string, error) {
	logger := log.FromContext(ctx)

	configMap := &corev1.ConfigMap{}
	if err := m.supervisorClient.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}, configMap); err != nil {
		return "", fmt.Errorf("failed to get configmap %s/%s: %w", namespace, name, err)
	}

	// Check Data field first
	if data, ok := configMap.Data[key]; ok {
		logger.V(1).Info("Retrieved cloud-init data from configmap",
			"configmap", name, "key", key, "dataLength", len(data))
		return data, nil
	}

	// Check BinaryData field
	if data, ok := configMap.BinaryData[key]; ok {
		logger.V(1).Info("Retrieved cloud-init binary data from configmap",
			"configmap", name, "key", key, "dataLength", len(data))
		return string(data), nil
	}

	return "", fmt.Errorf("key %q not found in configmap %s/%s", key, namespace, name)
}

// getKeyOrDefault returns the key if not empty, otherwise returns the default
func getKeyOrDefault(key, defaultKey string) string {
	if key != "" {
		return key
	}
	return defaultKey
}

// addCloudInitToVM adds cloud-init disk and volume to the existing slices
func (m *VMManager) addCloudInitToVM(ctx context.Context, machine *vitistackv1alpha1.Machine,
	disks []kubevirtv1.Disk, volumes []kubevirtv1.Volume) ([]kubevirtv1.Disk, []kubevirtv1.Volume, error) {

	cloudInitDisk, cloudInitVolume, err := m.buildCloudInitVolume(ctx, machine)
	if err != nil {
		return disks, volumes, err
	}

	if cloudInitDisk != nil && cloudInitVolume != nil {
		disks = append(disks, *cloudInitDisk)
		volumes = append(volumes, *cloudInitVolume)
	}

	return disks, volumes, nil
}
