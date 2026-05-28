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
	"net/url"
	"strings"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// LabelSharedISOVersion marks a DataVolume/PVC as a shared boot-ISO image and
	// carries its derived version name. Used to find shared volumes during GC and
	// to distinguish them from per-machine PVCs (which carry LabelSourceMachine).
	LabelSharedISOVersion = "vitistack.io/shared-iso-version"

	// AnnotationISOSourceURL records the OS.ImageID URL the shared DataVolume was
	// imported from, so a version label that later points at a different URL can
	// be detected and logged.
	AnnotationISOSourceURL = "vitistack.io/iso-source-url"
)

// IsSharedBootISOEnabled reports whether boot ISOs should be provisioned as a
// single shared, version-named DataVolume/PVC per namespace rather than a
// per-VM DataVolumeTemplate.
func IsSharedBootISOEnabled() bool {
	return viper.GetBool(consts.SHARED_BOOT_ISO)
}

// resolveISOStorageClass returns the storage class for boot-ISO volumes. Boot
// ISOs may need a different class than root disks (e.g. a shared RWX ISO must
// live on CephFS while root disks use Ceph RBD), so an ISO-specific override is
// honoured first. Resolution: STORAGE_CLASS_NAME_ISO if set, else
// STORAGE_CLASS_NAME, else "" (the cluster's default storage class).
func resolveISOStorageClass() string {
	if sc := viper.GetString(consts.STORAGE_CLASS_NAME_ISO); sc != "" {
		return sc
	}
	return viper.GetString(consts.STORAGE_CLASS_NAME)
}

// knownArches are the architecture tokens recognised in an ISO filename so they
// can be split out from the image variant (e.g. "nocloud-amd64" -> "amd64").
var knownArches = map[string]struct{}{
	"amd64":  {},
	"arm64":  {},
	"x86_64": {},
}

// imageExtensions are stripped from an ISO filename before parsing the variant.
var imageExtensions = []string{".iso", ".img", ".raw", ".qcow2", ".xz", ".gz", ".zst"}

// parseImageURL extracts the version, variant and architecture from a boot image
// URL of the conventional shape ".../<version>/<variant>-<arch>.<ext>", e.g.
// "https://factory.talos.dev/image/<hash>/v1.13.2/nocloud-amd64.iso" yields
// ("v1.13.2", "nocloud", "amd64"). Any component that can't be determined is
// returned empty; callers fall back to the Machine's OS spec fields.
func parseImageURL(imageURL string) (version, variant, arch string) {
	u, err := url.Parse(imageURL)
	if err != nil || u.Path == "" {
		return "", "", ""
	}

	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) == 0 {
		return "", "", ""
	}

	filename := segments[len(segments)-1]
	for _, ext := range imageExtensions {
		filename = strings.TrimSuffix(filename, ext)
	}
	// Drop any remaining extension (e.g. nocloud-amd64.iso.xz already trimmed once).
	if dot := strings.IndexByte(filename, '.'); dot >= 0 {
		filename = filename[:dot]
	}

	tokens := strings.Split(filename, "-")
	if len(tokens) > 1 {
		if _, ok := knownArches[tokens[len(tokens)-1]]; ok {
			arch = tokens[len(tokens)-1]
			variant = strings.Join(tokens[:len(tokens)-1], "-")
		} else {
			variant = filename
		}
	} else {
		variant = filename
	}

	// The path segment before the filename is conventionally the version.
	if len(segments) >= 2 {
		candidate := segments[len(segments)-2]
		if looksLikeVersion(candidate) {
			version = candidate
		}
	}

	return version, variant, arch
}

// looksLikeVersion reports whether s resembles a version string (starts with
// 'v' followed by a digit, or starts with a digit).
func looksLikeVersion(s string) bool {
	if s == "" {
		return false
	}
	if s[0] >= '0' && s[0] <= '9' {
		return true
	}
	return len(s) >= 2 && s[0] == 'v' && s[1] >= '0' && s[1] <= '9'
}

// SharedISOResourceName derives a deterministic, DNS-1123 name for the shared
// boot-ISO volume from the Machine's OS spec and image URL. Every VM that boots
// the same image label shares one PVC under this name, e.g.
// "talos-v1.13.2-nocloud-amd64". Explicit OS spec fields take precedence over
// values parsed from the URL.
func SharedISOResourceName(machine *vitistackv1alpha1.Machine) string {
	urlVersion, variant, urlArch := parseImageURL(machine.Spec.OS.ImageID)

	version := machine.Spec.OS.Version
	if version == "" {
		version = urlVersion
	}
	arch := machine.Spec.OS.Architecture
	if arch == "" {
		arch = urlArch
	}

	prefix := strings.ToLower(machine.Spec.OS.Distribution)
	if prefix == "" {
		// No distribution declared: use the image variant as the prefix when it
		// is meaningful, otherwise a generic marker so the name is never empty.
		if variant != "" {
			prefix = variant
			variant = ""
		} else {
			prefix = "bootiso"
		}
	}

	parts := []string{prefix}
	for _, p := range []string{version, variant, arch} {
		if p != "" {
			parts = append(parts, p)
		}
	}

	return sanitizeDNS1123(strings.Join(parts, "-"))
}

// sanitizeDNS1123 lowercases the name and replaces any character outside the
// DNS-1123 subdomain set [a-z0-9.-] with '-', collapsing runs of substituted
// characters and trimming leading/trailing separators so the result is a valid
// Kubernetes object name. Dots are preserved so version strings like "v1.13.2"
// stay human-readable.
func sanitizeDNS1123(name string) string {
	name = strings.ToLower(name)
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "bootiso"
	}
	return out
}

// EnsureSharedISODataVolume ensures the shared, version-named boot-ISO
// DataVolume exists in the machine's namespace and reports whether its import
// has completed (phase Succeeded). VMs must not be created referencing the
// shared PVC until import is ready, so callers requeue while ready is false.
// Returns the resolved DataVolume/PVC name in all cases.
func (m *VMManager) EnsureSharedISODataVolume(ctx context.Context, machine *vitistackv1alpha1.Machine) (ready bool, name string, err error) {
	logger := log.FromContext(ctx)

	name = SharedISOResourceName(machine)
	namespace := machine.Namespace
	key := types.NamespacedName{Name: name, Namespace: namespace}

	existing := &cdiv1.DataVolume{}
	getErr := m.remoteClient.Get(ctx, key, existing)
	if getErr == nil {
		// Detect a version label reused for a different source URL — the content
		// would be stale. We keep serving the existing volume (the version string
		// is the contract) but surface it loudly.
		if src := existing.Annotations[AnnotationISOSourceURL]; src != "" && src != machine.Spec.OS.ImageID {
			logger.Info("Shared boot-ISO volume already exists with a different source URL; keeping existing import",
				"name", name, "existingURL", src, "requestedURL", machine.Spec.OS.ImageID)
		}
		return existing.Status.Phase == cdiv1.Succeeded, name, nil
	}
	if !errors.IsNotFound(getErr) {
		return false, name, fmt.Errorf("failed to get shared boot-ISO DataVolume %q: %w", name, getErr)
	}

	dv, buildErr := m.buildSharedISODataVolume(ctx, machine, name)
	if buildErr != nil {
		return false, name, buildErr
	}

	if createErr := m.remoteClient.Create(ctx, dv); createErr != nil {
		// Concurrent reconciles (MaxConcurrentReconciles > 1) racing on the same
		// version: treat AlreadyExists as success and let the next pass observe
		// readiness.
		if errors.IsAlreadyExists(createErr) {
			logger.V(1).Info("Shared boot-ISO DataVolume already created concurrently", "name", name)
			return false, name, nil
		}
		return false, name, fmt.Errorf("failed to create shared boot-ISO DataVolume %q: %w", name, createErr)
	}

	logger.Info("Created shared boot-ISO DataVolume", "name", name, "namespace", namespace, "url", machine.Spec.OS.ImageID)
	// Just created — import has not completed yet.
	return false, name, nil
}

// buildSharedISODataVolume constructs the standalone DataVolume spec for a
// shared boot ISO. It mirrors the per-VM template logic for size (HEAD request)
// and Filesystem mode, but is owned by neither a VM nor a Machine so its
// lifecycle is decoupled and reusable. Unlike the per-VM ISO, the access mode is
// taken from SHARED_BOOT_ISO_ACCESS_MODE (default ReadWriteMany) rather than
// negotiated from the StorageProfile: a shared volume must be RWX to attach
// across nodes, and letting a profile that only advertises RWO win would
// silently produce an unsharable PVC.
func (m *VMManager) buildSharedISODataVolume(ctx context.Context, machine *vitistackv1alpha1.Machine, name string) (*cdiv1.DataVolume, error) {
	logger := log.FromContext(ctx)

	sourceType := machine.Annotations["kubevirt.io/boot-source-type"]
	if sourceType == "" {
		sourceType = SourceTypeHTTP
	}

	storageSize := getISOStorageSize(ctx, machine.Spec.OS.ImageID, sourceType)

	volumeMode := corev1.PersistentVolumeFilesystem
	if modeStr := viper.GetString(consts.SHARED_ISO_PVC_VOLUME_MODE); modeStr != "" {
		volumeMode = corev1.PersistentVolumeMode(modeStr)
	}

	storageClassName := resolveISOStorageClass()

	accessMode := corev1.PersistentVolumeAccessMode(viper.GetString(consts.SHARED_BOOT_ISO_ACCESS_MODE))
	if accessMode == "" {
		accessMode = corev1.ReadWriteMany
	}
	if accessMode != corev1.ReadWriteMany {
		logger.Info("Shared boot ISO configured with a non-RWX access mode; VMs on different nodes will not be able to share it",
			"accessMode", accessMode, "name", name)
	}
	accessModes := []corev1.PersistentVolumeAccessMode{accessMode}

	dv := &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: machine.Namespace,
			Labels: map[string]string{
				vitistackv1alpha1.ManagedByAnnotation: viper.GetString(consts.MANAGED_BY),
				LabelSharedISOVersion:                 name,
			},
			Annotations: map[string]string{
				AnnotationISOSourceURL: machine.Spec.OS.ImageID,
			},
		},
		Spec: cdiv1.DataVolumeSpec{
			Storage: &cdiv1.StorageSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: storageSize,
					},
				},
				VolumeMode:  &volumeMode,
				AccessModes: accessModes,
			},
		},
	}

	if storageClassName != "" {
		dv.Spec.Storage.StorageClassName = &storageClassName
	}

	switch sourceType {
	case SourceTypeHTTP, SourceTypeHTTPS:
		dv.Spec.Source = &cdiv1.DataVolumeSource{
			HTTP: &cdiv1.DataVolumeSourceHTTP{URL: machine.Spec.OS.ImageID},
		}
	case "registry":
		dv.Spec.Source = &cdiv1.DataVolumeSource{
			Registry: &cdiv1.DataVolumeSourceRegistry{URL: &machine.Spec.OS.ImageID},
		}
	default:
		return nil, fmt.Errorf("unsupported boot-source-type %q for shared boot ISO", sourceType)
	}

	return dv, nil
}

// GCOrphanedSharedISOs deletes shared boot-ISO DataVolumes in the namespace
// that are no longer referenced by any VirtualMachine. Deleting the DataVolume
// cascades to its backing PVC. This is a no-op when the shared-ISO feature is
// disabled. It is best-effort: errors are logged and a partial sweep is fine,
// since the next machine deletion in the namespace retries.
//
// Call this only after the triggering VM has been deleted, so its reference no
// longer counts toward keeping a shared volume alive.
func (m *VMManager) GCOrphanedSharedISOs(ctx context.Context, namespace string) {
	logger := log.FromContext(ctx)

	if !IsSharedBootISOEnabled() {
		return
	}

	dvList := &cdiv1.DataVolumeList{}
	if err := m.remoteClient.List(ctx, dvList,
		client.InNamespace(namespace),
		client.HasLabels{LabelSharedISOVersion},
	); err != nil {
		logger.Error(err, "Failed to list shared boot-ISO DataVolumes for GC", "namespace", namespace)
		return
	}
	if len(dvList.Items) == 0 {
		return
	}

	referenced, err := m.referencedPVCClaimNames(ctx, namespace)
	if err != nil {
		logger.Error(err, "Failed to list VMs for shared boot-ISO GC; skipping", "namespace", namespace)
		return
	}

	for i := range dvList.Items {
		dv := &dvList.Items[i]
		if _, used := referenced[dv.Name]; used {
			continue
		}
		if err := m.remoteClient.Delete(ctx, dv); err != nil && !errors.IsNotFound(err) {
			logger.Error(err, "Failed to delete orphaned shared boot-ISO DataVolume", "name", dv.Name, "namespace", namespace)
			continue
		}
		logger.Info("Garbage-collected orphaned shared boot-ISO DataVolume", "name", dv.Name, "namespace", namespace)
	}
}

// referencedPVCClaimNames returns the set of PVC claim names referenced by any
// VirtualMachine in the namespace — both directly via PersistentVolumeClaim
// volume sources and via DataVolume volume sources (whose backing PVC shares the
// DataVolume's name). Used to decide whether a shared boot-ISO volume is still
// in use.
func (m *VMManager) referencedPVCClaimNames(ctx context.Context, namespace string) (map[string]struct{}, error) {
	vmList := &kubevirtv1.VirtualMachineList{}
	if err := m.remoteClient.List(ctx, vmList, client.InNamespace(namespace)); err != nil {
		return nil, err
	}

	referenced := make(map[string]struct{})
	for i := range vmList.Items {
		tmpl := vmList.Items[i].Spec.Template
		if tmpl == nil {
			continue
		}
		for j := range tmpl.Spec.Volumes {
			vol := &tmpl.Spec.Volumes[j]
			if vol.PersistentVolumeClaim != nil {
				referenced[vol.PersistentVolumeClaim.ClaimName] = struct{}{}
			}
			if vol.DataVolume != nil {
				referenced[vol.DataVolume.Name] = struct{}{}
			}
		}
	}
	return referenced, nil
}
