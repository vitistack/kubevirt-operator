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
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

const (
	testVersion = "v1.13.2"
	testArchAMD = "amd64"
	testArchARM = "arm64"
	testDistro  = "talos"
	testISOURL  = "https://factory.talos.dev/image/abc/v1.13.2/nocloud-amd64.iso"
	testBootISO = "bootiso"
)

func machineWithOS(os *vitistackv1alpha1.MachineOS, annotations map[string]string) *vitistackv1alpha1.Machine {
	return &vitistackv1alpha1.Machine{
		Spec:       vitistackv1alpha1.MachineSpec{OS: *os},
		ObjectMeta: metav1.ObjectMeta{Annotations: annotations},
	}
}

func findVolume(volumes []kubevirtv1.Volume, name string) *kubevirtv1.Volume {
	for i := range volumes {
		if volumes[i].Name == name {
			return &volumes[i]
		}
	}
	return nil
}

func TestParseImageURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		wantVersion string
		wantVariant string
		wantArch    string
	}{
		{
			name:        "talos factory nocloud",
			url:         testISOURL,
			wantVersion: testVersion,
			wantVariant: "nocloud",
			wantArch:    testArchAMD,
		},
		{
			name:        "arm64 metal",
			url:         "https://factory.talos.dev/image/abc/v1.14.0/metal-arm64.iso",
			wantVersion: "v1.14.0",
			wantVariant: "metal",
			wantArch:    testArchARM,
		},
		{
			name:        "no recognised arch",
			url:         "https://example.com/images/v1.0.0/ubuntu.img",
			wantVersion: "v1.0.0",
			wantVariant: "ubuntu",
			wantArch:    "",
		},
		{
			name:        "compound variant",
			url:         "https://example.com/x/1.2.3/foo-bar-amd64.iso",
			wantVersion: "1.2.3",
			wantVariant: "foo-bar",
			wantArch:    "amd64",
		},
		{
			name:        "empty url",
			url:         "",
			wantVersion: "",
			wantVariant: "",
			wantArch:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			version, variant, arch := parseImageURL(tt.url)
			if version != tt.wantVersion || variant != tt.wantVariant || arch != tt.wantArch {
				t.Errorf("parseImageURL(%q) = (%q,%q,%q); want (%q,%q,%q)",
					tt.url, version, variant, arch, tt.wantVersion, tt.wantVariant, tt.wantArch)
			}
		})
	}
}

func TestSharedISOResourceName(t *testing.T) {
	tests := []struct {
		name string
		os   vitistackv1alpha1.MachineOS
		want string
	}{
		{
			name: "distribution + version + url variant/arch",
			os: vitistackv1alpha1.MachineOS{
				Distribution: testDistro,
				ImageID:      testISOURL,
			},
			want: "talos-v1.13.2-nocloud-amd64",
		},
		{
			name: "explicit version/arch override URL",
			os: vitistackv1alpha1.MachineOS{
				Distribution: testDistro,
				Version:      "v1.13.3",
				Architecture: testArchARM,
				ImageID:      testISOURL,
			},
			want: "talos-v1.13.3-nocloud-arm64",
		},
		{
			name: "no distribution falls back to variant prefix",
			os: vitistackv1alpha1.MachineOS{
				ImageID: "https://example.com/x/v2.0.0/nocloud-amd64.iso",
			},
			want: "nocloud-v2.0.0-amd64",
		},
		{
			name: "sanitizes unsafe characters",
			os: vitistackv1alpha1.MachineOS{
				Distribution: "Talos_OS",
				Version:      "v1.13.2",
			},
			want: "talos-os-v1.13.2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SharedISOResourceName(machineWithOS(&tt.os, nil))
			if got != tt.want {
				t.Errorf("SharedISOResourceName() = %q; want %q", got, tt.want)
			}
		})
	}
}

func TestSanitizeDNS1123(t *testing.T) {
	tests := map[string]string{
		"Talos-V1.13.2":   "talos-v1.13.2",
		"--leading--":     "leading",
		"a__b//c":         "a-b-c",
		"":                testBootISO,
		"v1.13.2-nocloud": "v1.13.2-nocloud",
	}
	for in, want := range tests {
		if got := sanitizeDNS1123(in); got != want {
			t.Errorf("sanitizeDNS1123(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestAddISOBootSource_SharedVsPerVM(t *testing.T) {
	machine := machineWithOS(&vitistackv1alpha1.MachineOS{
		Distribution: testDistro,
		Version:      testVersion,
		Architecture: testArchAMD,
		ImageID:      testISOURL,
	}, map[string]string{AnnotationBootSource: BootSourceDataVolume})

	m := &VMManager{}

	t.Run("shared mode references read-only PVC", func(t *testing.T) {
		viper.Set(consts.SHARED_BOOT_ISO, true)
		defer viper.Set(consts.SHARED_BOOT_ISO, false)

		_, volumes := m.addISOBootSource(nil, nil, machine, "talos-cluster-ctp0")
		vol := findVolume(volumes, CDROMVolumeName)
		if vol == nil {
			t.Fatalf("CDROM volume not found")
		}
		if vol.PersistentVolumeClaim == nil {
			t.Fatalf("expected PersistentVolumeClaim source in shared mode, got %+v", vol.VolumeSource)
		}
		// Must NOT be read-only: a read-only Filesystem-mode mount breaks
		// virt-launcher's ownership chown and prevents VMI startup. The CDRom
		// device type provides the write protection instead.
		if vol.PersistentVolumeClaim.ReadOnly {
			t.Errorf("shared boot ISO PVC must not be mounted read-only (breaks Filesystem chown)")
		}
		if want := "talos-v1.13.2-nocloud-amd64"; vol.PersistentVolumeClaim.ClaimName != want {
			t.Errorf("ClaimName = %q; want %q", vol.PersistentVolumeClaim.ClaimName, want)
		}
		if dvts := m.buildDataVolumeTemplates(context.Background(), machine, "talos-cluster-ctp0"); dvts != nil {
			t.Errorf("expected no DataVolumeTemplates in shared mode, got %d", len(dvts))
		}
	})

	t.Run("per-VM mode references DataVolume", func(t *testing.T) {
		viper.Set(consts.SHARED_BOOT_ISO, false)

		_, volumes := m.addISOBootSource(nil, nil, machine, "talos-cluster-ctp0")
		vol := findVolume(volumes, CDROMVolumeName)
		if vol == nil {
			t.Fatalf("CDROM volume not found")
		}
		if vol.DataVolume == nil {
			t.Fatalf("expected DataVolume source in per-VM mode, got %+v", vol.VolumeSource)
		}
		if want := "talos-cluster-ctp0-iso"; vol.DataVolume.Name != want {
			t.Errorf("DataVolume name = %q; want %q", vol.DataVolume.Name, want)
		}
	})
}
