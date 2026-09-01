package vm

import (
	"context"
	"testing"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

func TestAddSpreadConstraints(t *testing.T) {
	testcases := []struct {
		desc      string
		clusterID string
		nodeRole  string // worker or controlplane
	}{
		{
			desc:      "worker machine",
			clusterID: "clusterid1",
			nodeRole:  "worker",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.desc, func(t *testing.T) {
			wantClusterID := tc.clusterID
			wantNodeRole := tc.nodeRole

			machine := &vitistackv1alpha1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name: "testMachine",
					Labels: map[string]string{
						vitistackv1alpha1.ClusterIdAnnotation: wantClusterID,
						vitistackv1alpha1.NodeRoleAnnotation:  wantNodeRole, // control-plane, worker
					},
				},
			}

			var vmm *VMManager
			p := &vmBuildParams{
				vmName:               "testVM",
				machine:              machine,
				disks:                []kubevirtv1.Disk{{Name: "testDisk", DiskDevice: kubevirtv1.DiskDevice{}}},
				volumes:              []kubevirtv1.Volume{{Name: "testVol", VolumeSource: kubevirtv1.VolumeSource{}}},
				memoryRequest:        "1024",
				coresRequest:         1,
				socketsRequest:       2,
				threadsRequest:       2,
				networkConfiguration: &kubevirtv1.Network{},
				networkBootOrder:     new(uint(1)),
				macAddress:           "test:mac",
			}

			vm := vmm.buildVMSpec(context.Background(), p)

			// vm = addSpreadConstraints(machine, vm)
			vm = addAntiAffinity(machine, vm)

			gotClusterID, found := vm.Spec.Template.ObjectMeta.Labels[vitistackv1alpha1.ClusterIdAnnotation]
			if !found {
				t.Fatalf("cluster id annotation missing: %+v", vm.Spec.Template.ObjectMeta.Labels)
			}
			if gotClusterID != wantClusterID {
				t.Errorf("got %q, want %q ", gotClusterID, wantClusterID)
			}

			gotNodeRole, found := vm.Spec.Template.ObjectMeta.Labels[vitistackv1alpha1.NodeRoleAnnotation]
			if !found {
				t.Fatalf("node role annotation missing")
			}
			if gotNodeRole != wantNodeRole {
				t.Errorf("got %q, want %q ", gotNodeRole, wantNodeRole)
			}

			if len(vm.Spec.Template.Spec.TopologySpreadConstraints) < 1 {
				t.Fatalf("spread constraints were not assigned")
			}

			t.Logf("%+v", vm.Spec.Template.Spec.TopologySpreadConstraints)
		})
	}
}
