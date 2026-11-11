package vm

import (
	"context"
	"testing"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newPlacementVM() *kubevirtv1.VirtualMachine {
	return &kubevirtv1.VirtualMachine{Spec: kubevirtv1.VirtualMachineSpec{
		Template: &kubevirtv1.VirtualMachineInstanceTemplateSpec{},
	}}
}

// TestMachineEnvironment pins where the environment comes from: the
// KubernetesCluster that owns the machine, and "" whenever it cannot be read.
func TestMachineEnvironment(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := vitistackv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	cluster := func(name, env string) *vitistackv1alpha1.KubernetesCluster {
		c := &vitistackv1alpha1.KubernetesCluster{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}}
		c.Spec.Cluster.Environment = env
		return c
	}
	owned := func(owner string) *vitistackv1alpha1.Machine {
		m := &vitistackv1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: "ns"}}
		if owner != "" {
			m.OwnerReferences = []metav1.OwnerReference{{Kind: "KubernetesCluster", Name: owner}}
		}
		return m
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(cluster("prod", " Prod "), cluster("test", "test")).Build()
	m := &VMManager{supervisorClient: cl}

	for _, tc := range []struct {
		name  string
		owner string
		want  string
	}{
		{"declared, trimmed and lower-cased", "prod", "prod"},
		{"non-production", "test", "test"},
		{"owner missing in the cluster", "gone", ""},
		{"no owner reference", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := m.machineEnvironment(context.Background(), owned(tc.owner)); got != tc.want {
				t.Fatalf("environment = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestApplyPlacement pins the label every VM gets and which VMs get the pool
// rule, in which form.
func TestApplyPlacement(t *testing.T) {
	const pool = "example.io/pool=prod"
	for _, tc := range []struct {
		name      string
		env       string
		pool      string
		mode      string
		wantLabel string
		want      string // "", "required" or "preferred"
	}{
		{"production, required by default", "prod", pool, "", "prod", PlacementRequired},
		{"second spelling", "production", pool, PlacementRequired, "production", PlacementRequired},
		{"unknown counts as production", "", pool, PlacementRequired, EnvironmentUnknown, PlacementRequired},
		{"preferred mode", "prod", pool, PlacementPreferred, "prod", PlacementPreferred},
		{"non-production is not steered", "test", pool, PlacementRequired, "test", ""},
		{"no pool configured", "prod", "", PlacementRequired, "prod", ""},
		{"malformed pool is ignored", "prod", "no-equals-sign", PlacementRequired, "prod", ""},
		{"invalid label value", "not a label!", "", PlacementRequired, EnvironmentUnknown, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viper.Set(consts.PROD_POOL_NODE_LABEL, tc.pool)
			viper.Set(consts.PROD_POOL_PLACEMENT, tc.mode)
			t.Cleanup(viper.Reset)

			vm := applyPlacement(context.Background(), newPlacementVM(), tc.env)

			if got := vm.Labels[LabelEnvironment]; got != tc.wantLabel {
				t.Errorf("VM label = %q, want %q", got, tc.wantLabel)
			}
			if got := vm.Spec.Template.ObjectMeta.Labels[LabelEnvironment]; got != tc.wantLabel {
				t.Errorf("template label = %q, want %q", got, tc.wantLabel)
			}

			aff := vm.Spec.Template.Spec.Affinity
			switch tc.want {
			case "":
				if aff != nil {
					t.Fatalf("affinity = %+v, want none", aff)
				}
			case PlacementRequired:
				if aff == nil || aff.NodeAffinity == nil || aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
					t.Fatalf("want required node affinity, got %+v", aff)
				}
				if len(aff.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 0 {
					t.Errorf("unexpected preferred terms")
				}
				req := aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions[0]
				if req.Key != "example.io/pool" || req.Operator != corev1.NodeSelectorOpIn || len(req.Values) != 1 || req.Values[0] != "prod" {
					t.Errorf("requirement = %+v", req)
				}
			case PlacementPreferred:
				if aff == nil || aff.NodeAffinity == nil || len(aff.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
					t.Fatalf("want one preferred term, got %+v", aff)
				}
				if aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
					t.Errorf("unexpected required terms")
				}
			}
		})
	}
}

// TestApplyPlacementKeepsPodAntiAffinity: the anti-affinity placement policy
// sets the affinity block first; the pool rule must join it, not replace it.
func TestApplyPlacementKeepsPodAntiAffinity(t *testing.T) {
	viper.Set(consts.PROD_POOL_NODE_LABEL, "example.io/pool=prod")
	t.Cleanup(viper.Reset)

	machine := &vitistackv1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m", Labels: map[string]string{
		vitistackv1alpha1.ClusterIdAnnotation: "c1", vitistackv1alpha1.NodeRoleAnnotation: "worker",
	}}}
	vm := newPlacementVM()
	vm.Spec.Template.ObjectMeta.Labels = map[string]string{}
	vm = applyPlacement(context.Background(), addAntiAffinity(machine, vm), "prod")

	aff := vm.Spec.Template.Spec.Affinity
	if aff == nil || aff.PodAntiAffinity == nil {
		t.Fatalf("pod anti-affinity was dropped: %+v", aff)
	}
	if aff.NodeAffinity == nil {
		t.Fatalf("node affinity missing: %+v", aff)
	}
}
