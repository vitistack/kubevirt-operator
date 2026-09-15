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
	"testing"

	"github.com/spf13/viper"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubevirtv1 "kubevirt.io/api/core/v1"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	gcTestNamespace = "vitistack-test"
	gcTestISOName   = "talos-v1.13.10-nocloud-amd64"
)

func newGCTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kubevirtv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add kubevirt scheme: %v", err)
	}
	if err := cdiv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add cdi scheme: %v", err)
	}
	return scheme
}

func sharedISODataVolume() *cdiv1.DataVolume {
	return &cdiv1.DataVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:      gcTestISOName,
			Namespace: gcTestNamespace,
			Labels:    map[string]string{LabelSharedISOVersion: gcTestISOName},
		},
	}
}

func cdromVolume(claimName string) kubevirtv1.Volume {
	return kubevirtv1.Volume{
		Name: CDROMVolumeName,
		VolumeSource: kubevirtv1.VolumeSource{
			PersistentVolumeClaim: &kubevirtv1.PersistentVolumeClaimVolumeSource{
				PersistentVolumeClaimVolumeSource: corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: claimName,
				},
			},
		},
	}
}

func vmWithVolumes(name string, volumes ...kubevirtv1.Volume) *kubevirtv1.VirtualMachine {
	return &kubevirtv1.VirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gcTestNamespace},
		Spec: kubevirtv1.VirtualMachineSpec{
			Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{
				Spec: kubevirtv1.VirtualMachineInstanceSpec{Volumes: volumes},
			},
		},
	}
}

func vmiWithVolumes(name string, volumes ...kubevirtv1.Volume) *kubevirtv1.VirtualMachineInstance {
	return &kubevirtv1.VirtualMachineInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: gcTestNamespace},
		Spec:       kubevirtv1.VirtualMachineInstanceSpec{Volumes: volumes},
	}
}

func TestGCOrphanedSharedISOs(t *testing.T) {
	tests := []struct {
		name     string
		objs     []client.Object
		wantKept bool
	}{
		{
			name:     "deleted when nothing references it",
			wantKept: false,
		},
		{
			name:     "kept while a VM template references it",
			objs:     []client.Object{vmWithVolumes("vm-installing", cdromVolume(gcTestISOName))},
			wantKept: true,
		},
		{
			// After os-installed the CDROM is stripped from the VM template, but
			// the running VMI keeps the shared PVC mounted until its next restart.
			// Deleting the DataVolume then cascades to a PVC that cannot finish
			// terminating (kubernetes.io/pvc-protection) and wedges the namespace.
			name: "kept while a running VMI still mounts it after the template was detached",
			objs: []client.Object{
				vmWithVolumes("vm-installed"),
				vmiWithVolumes("vm-installed", cdromVolume(gcTestISOName)),
			},
			wantKept: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			viper.Set(consts.SHARED_BOOT_ISO, true)
			defer viper.Set(consts.SHARED_BOOT_ISO, false)

			c := fake.NewClientBuilder().
				WithScheme(newGCTestScheme(t)).
				WithObjects(append(tt.objs, sharedISODataVolume())...).
				Build()
			m := &VMManager{}
			m.SetRemoteClient(c)

			m.GCOrphanedSharedISOs(context.Background(), gcTestNamespace)

			err := c.Get(context.Background(),
				types.NamespacedName{Name: gcTestISOName, Namespace: gcTestNamespace}, &cdiv1.DataVolume{})
			switch {
			case tt.wantKept && err != nil:
				t.Errorf("shared ISO DataVolume was deleted, want kept (err=%v)", err)
			case !tt.wantKept && !errors.IsNotFound(err):
				t.Errorf("shared ISO DataVolume still present, want garbage-collected (err=%v)", err)
			}
		})
	}
}
