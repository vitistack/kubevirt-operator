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

package status

import (
	"context"
	"testing"
	"time"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const (
	testMachineName      = "worker-0"
	testMachineNamespace = "vitistack-test"

	// The fake client assigns this resourceVersion to seeded objects and
	// increments it by one on every successful write.
	seededResourceVersion    = "999"
	afterOneWriteRV          = "1000"
	previousLastUpdatedStamp = "2026-01-01T00:00:00Z"

	running          = "Running"
	providerKubevirt = "kubevirt"
	oldIP            = "10.0.0.5"
	newIP            = "10.0.0.6"
)

func previousLastUpdated(t *testing.T) metav1.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, previousLastUpdatedStamp)
	if err != nil {
		t.Fatalf("parse timestamp: %v", err)
	}
	return metav1.NewTime(ts)
}

// storedRunningStatus is what a previous reconcile already wrote for a
// running VM, as the API server hands it back (nil for omitted lists).
func storedRunningStatus(t *testing.T) *vitistackv1alpha1.MachineStatus {
	t.Helper()
	return &vitistackv1alpha1.MachineStatus{
		Phase:              running,
		State:              running,
		Provider:           providerKubevirt,
		LastUpdated:        previousLastUpdated(t),
		IPAddresses:        []string{oldIP},
		PrivateIPAddresses: []string{oldIP},
		NetworkInterfaces: []vitistackv1alpha1.NetworkInterfaceStatus{{
			MACAddress:  "02:00:00:00:00:01",
			IPAddresses: []string{oldIP},
			State:       "up",
		}},
		Disks: []vitistackv1alpha1.MachineStatusDisk{{
			Name:        "root",
			Device:      "/dev/vda",
			PVCName:     "worker-0-root",
			VolumeMode:  "Block",
			Size:        21474836480,
			AccessModes: []string{"ReadWriteMany"},
		}},
	}
}

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := vitistackv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add vitistack scheme: %v", err)
	}
	return scheme
}

func newTestClient(t *testing.T, stored *vitistackv1alpha1.MachineStatus, funcs *interceptor.Funcs) client.Client {
	t.Helper()
	seed := &vitistackv1alpha1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: testMachineName, Namespace: testMachineNamespace},
		Status:     *stored,
	}
	b := fake.NewClientBuilder().
		WithScheme(newTestScheme(t)).
		WithObjects(seed).
		WithStatusSubresource(&vitistackv1alpha1.Machine{})
	if funcs != nil {
		b = b.WithInterceptorFuncs(*funcs)
	}
	return b.Build()
}

// getMachine reads the Machine the way the reconciler does at the start of a pass.
func getMachine(t *testing.T, c client.Client) *vitistackv1alpha1.Machine {
	t.Helper()
	m := &vitistackv1alpha1.Machine{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: testMachineName, Namespace: testMachineNamespace}, m); err != nil {
		t.Fatalf("get machine: %v", err)
	}
	return m
}

// recomputeRunningStatus mimics evaluateState for an unchanged running VM:
// every field is rebuilt from the VMI, producing freshly allocated (and, for
// lists with no entries, empty rather than nil) slices.
func recomputeRunningStatus(machine *vitistackv1alpha1.Machine) {
	machine.Status.Phase = running
	machine.Status.State = running
	machine.Status.Provider = providerKubevirt
	machine.Status.IPAddresses = []string{oldIP}
	machine.Status.IPv6Addresses = []string{}
	machine.Status.PublicIPAddresses = []string{}
	machine.Status.PrivateIPAddresses = []string{oldIP}
	machine.Status.NetworkInterfaces = []vitistackv1alpha1.NetworkInterfaceStatus{{
		MACAddress:    "02:00:00:00:00:01",
		IPAddresses:   []string{oldIP},
		IPv6Addresses: []string{},
		State:         "up",
	}}
	machine.Status.Disks = []vitistackv1alpha1.MachineStatusDisk{{
		Name:        "root",
		Device:      "/dev/vda",
		PVCName:     "worker-0-root",
		VolumeMode:  "Block",
		Size:        21474836480,
		AccessModes: []string{"ReadWriteMany"},
	}}
}

// A steady-state reconcile of an unchanged VM must not write the Machine
// status: a timestamp-only write fans out as a watch event to every Machine
// watcher (talos-operator re-queues the owning cluster on each one).
func TestUpdateMachineStatus_SkipsWriteWhenOnlyLastUpdatedWouldChange(t *testing.T) {
	c := newTestClient(t, storedRunningStatus(t), nil)
	sm := NewManager(c, nil)

	// Two consecutive reconciles of the same running VM.
	for pass := 1; pass <= 2; pass++ {
		machine := getMachine(t, c)
		recomputeRunningStatus(machine)
		// Failure paths stamp LastUpdated themselves before calling in.
		machine.Status.LastUpdated = metav1.Now()

		if err := sm.UpdateMachineStatus(context.Background(), machine, running); err != nil {
			t.Fatalf("pass %d: UpdateMachineStatus: %v", pass, err)
		}

		if machine.ResourceVersion != seededResourceVersion {
			t.Errorf("pass %d: caller resourceVersion = %q, want %q", pass, machine.ResourceVersion, seededResourceVersion)
		}
		if !machine.Status.LastUpdated.Time.Equal(previousLastUpdated(t).Time) {
			t.Errorf("pass %d: caller status.lastUpdated = %s, want the stored %s", pass, machine.Status.LastUpdated.UTC().Format(time.RFC3339), previousLastUpdatedStamp)
		}
	}

	stored := getMachine(t, c)
	if stored.ResourceVersion != seededResourceVersion {
		t.Errorf("stored resourceVersion = %q, want %q (status must not be rewritten)", stored.ResourceVersion, seededResourceVersion)
	}
	if !stored.Status.LastUpdated.Time.Equal(previousLastUpdated(t).Time) {
		t.Errorf("stored status.lastUpdated = %s, want unchanged %s", stored.Status.LastUpdated.UTC().Format(time.RFC3339), previousLastUpdatedStamp)
	}
}

func TestUpdateMachineStatus_WritesRealChangesWithFreshLastUpdated(t *testing.T) {
	tests := []struct {
		name   string
		stored func(t *testing.T) *vitistackv1alpha1.MachineStatus
		mutate func(m *vitistackv1alpha1.Machine)
		check  func(t *testing.T, got *vitistackv1alpha1.MachineStatus)
	}{
		{
			name:   "new IP address",
			stored: storedRunningStatus,
			mutate: func(m *vitistackv1alpha1.Machine) {
				recomputeRunningStatus(m)
				m.Status.IPAddresses = []string{newIP}
			},
			check: func(t *testing.T, got *vitistackv1alpha1.MachineStatus) {
				if len(got.IPAddresses) != 1 || got.IPAddresses[0] != newIP {
					t.Errorf("stored ipAddresses = %v, want [10.0.0.6]", got.IPAddresses)
				}
			},
		},
		{
			name:   "phase transition",
			stored: storedRunningStatus,
			mutate: func(m *vitistackv1alpha1.Machine) {
				recomputeRunningStatus(m)
				m.Status.Phase = "Succeeded"
				m.Status.State = "Stopped"
			},
			check: func(t *testing.T, got *vitistackv1alpha1.MachineStatus) {
				if got.Phase != "Succeeded" || got.State != "Stopped" {
					t.Errorf("stored phase/state = %q/%q, want Succeeded/Stopped", got.Phase, got.State)
				}
			},
		},
		{
			// The provider is forced to kubevirt by the status manager itself,
			// so a stored status missing it is a real difference to write.
			name: "provider backfilled",
			stored: func(t *testing.T) *vitistackv1alpha1.MachineStatus {
				s := storedRunningStatus(t)
				s.Provider = ""
				return s
			},
			mutate: func(m *vitistackv1alpha1.Machine) {
				recomputeRunningStatus(m)
				m.Status.Provider = ""
			},
			check: func(t *testing.T, got *vitistackv1alpha1.MachineStatus) {
				if got.Provider != providerKubevirt {
					t.Errorf("stored provider = %q, want kubevirt", got.Provider)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, tt.stored(t), nil)
			sm := NewManager(c, nil)

			machine := getMachine(t, c)
			tt.mutate(machine)

			// metav1.Time is serialized with second precision.
			before := time.Now().Add(-time.Second)
			if err := sm.UpdateMachineStatus(context.Background(), machine, machine.Status.State); err != nil {
				t.Fatalf("UpdateMachineStatus: %v", err)
			}

			stored := getMachine(t, c)
			if stored.ResourceVersion != afterOneWriteRV {
				t.Errorf("stored resourceVersion = %q, want %q (exactly one write)", stored.ResourceVersion, afterOneWriteRV)
			}
			if !stored.Status.LastUpdated.After(before) {
				t.Errorf("stored status.lastUpdated = %s, want refreshed (after %s)", stored.Status.LastUpdated.UTC().Format(time.RFC3339), before.UTC().Format(time.RFC3339))
			}
			tt.check(t, &stored.Status)

			if machine.ResourceVersion != afterOneWriteRV {
				t.Errorf("caller resourceVersion = %q, want %q", machine.ResourceVersion, afterOneWriteRV)
			}
			if !machine.Status.LastUpdated.After(before) {
				t.Errorf("caller status.lastUpdated = %s, want refreshed", machine.Status.LastUpdated.UTC().Format(time.RFC3339))
			}
		})
	}
}

func TestUpdateMachineStatus_RetriesOnConflict(t *testing.T) {
	conflicts := 0
	funcs := &interceptor.Funcs{
		SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
			if conflicts == 0 {
				conflicts++
				return apierrors.NewConflict(schema.GroupResource{Group: "vitistack.io", Resource: "machines"}, obj.GetName(), nil)
			}
			return c.Status().Update(ctx, obj, opts...)
		},
	}
	c := newTestClient(t, storedRunningStatus(t), funcs)
	sm := NewManager(c, nil)

	machine := getMachine(t, c)
	recomputeRunningStatus(machine)
	machine.Status.IPAddresses = []string{newIP}

	if err := sm.UpdateMachineStatus(context.Background(), machine, running); err != nil {
		t.Fatalf("UpdateMachineStatus: %v", err)
	}
	if conflicts != 1 {
		t.Fatalf("injected conflicts = %d, want 1", conflicts)
	}
	stored := getMachine(t, c)
	if stored.ResourceVersion != afterOneWriteRV {
		t.Errorf("stored resourceVersion = %q, want %q", stored.ResourceVersion, afterOneWriteRV)
	}
	if len(stored.Status.IPAddresses) != 1 || stored.Status.IPAddresses[0] != newIP {
		t.Errorf("stored ipAddresses = %v, want [10.0.0.6]", stored.Status.IPAddresses)
	}
}
