package vm

import (
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

	// TODO: use buildVMSpec()

	virtualMachine := &kubevirtv1.VirtualMachine{}

	vm, err := addAntiAffinityRules(machine, virtualMachine)
	if err != nil {
		t.Fatalf("unexpected error: %v ", err)
	}

	gotClusterID, found := vm.Labels[vitistackv1alpha1.ClusterIdAnnotation]
	if !found {
		t.Errorf("cluster id annotation missing")
	}
	if gotClusterID != wantClusterID {
		t.Fatalf("got %q, want %q ", gotClusterID, wantClusterID)
	}

	gotNodeRole, found := vm.Labels[vitistackv1alpha1.NodeRoleAnnotation]
	if !found {
		t.Errorf("node role annotation missing")
	}
	if gotNodeRole != wantNodeRole {
		t.Fatalf("got %q, want %q ", gotNodeRole, wantNodeRole)
	}
}
