package vm

import (
	"context"
	"testing"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubevirtv1 "kubevirt.io/api/core/v1"
)

func TestAddAffinityRules(t *testing.T) {
	wantClusterID := "clusterid1"
	wantNodeRole := "worker"

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
		disks:                []kubevirtv1.Disk{{Name: "asdf", DiskDevice: kubevirtv1.DiskDevice{}}},
		volumes:              []kubevirtv1.Volume{{Name: "asdf", VolumeSource: kubevirtv1.VolumeSource{}}},
		memoryRequest:        "1024",
		coresRequest:         1,
		socketsRequest:       2,
		threadsRequest:       2,
		networkConfiguration: &kubevirtv1.Network{},
		networkBootOrder:     new(uint(1)),
		macAddress:           "asdf",
	}

	vm := vmm.buildVMSpec(context.Background(), p)

	vm, err := addSpreadConstraints(machine, vm)
	if err != nil {
		t.Fatalf("unexpected error: %v ", err)
	}

	gotClusterID, found := vm.Labels[vitistackv1alpha1.ClusterIdAnnotation]
	if !found {
		t.Fatalf("cluster id annotation missing: %+v", vm.ObjectMeta.Labels)
	}
	if gotClusterID != wantClusterID {
		t.Errorf("got %q, want %q ", gotClusterID, wantClusterID)
	}

	gotNodeRole, found := vm.Labels[vitistackv1alpha1.NodeRoleAnnotation]
	if !found {
		t.Fatalf("node role annotation missing")
	}
	if gotNodeRole != wantNodeRole {
		t.Errorf("got %q, want %q ", gotNodeRole, wantNodeRole)
	}
}
