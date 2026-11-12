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

const (
	envProd  = "prod"
	envTest  = "test"
	envMgmt  = "mgmt"
	testPool = "example.io/pool=prod"
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
		WithObjects(cluster("c-prod", " Prod "), cluster("c-test", envTest)).Build()

	for _, tc := range []struct {
		name  string
		owner string
		want  string
	}{
		{"declared, trimmed and lower-cased", "c-prod", envProd},
		{"non-production", "c-test", envTest},
		{"owner missing in the cluster", "gone", ""},
		{"no owner reference", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Both paths: the uncached reader main wires in, and the fallback.
			for _, m := range []*VMManager{{clusterReader: cl}, {supervisorClient: cl}} {
				if got := m.machineEnvironment(context.Background(), owned(tc.owner)); got != tc.want {
					t.Fatalf("environment = %q, want %q", got, tc.want)
				}
			}
		})
	}
}

// TestApplyPlacement pins the label every VM gets and which VMs get a pool
// rule, in which form and direction.
func TestApplyPlacement(t *testing.T) {
	const (
		reqIn     = PlacementRequired + " In"
		prefIn    = PlacementPreferred + " In"
		reqNotIn  = PlacementRequired + " NotIn"
		prefNotIn = PlacementPreferred + " NotIn"
	)
	for _, tc := range []struct {
		name      string
		env       string
		pool      string
		mode      string
		nonProd   string
		wantLabel string
		want      string // "" or "<form> <operator>"
	}{
		{"production, required", envProd, testPool, PlacementRequired, NonProdAllowed, envProd, reqIn},
		{"second spelling, labelled prod", "production", testPool, PlacementRequired, NonProdAllowed, envProd, reqIn},
		{"unknown counts as production", "", testPool, PlacementRequired, NonProdAllowed, EnvironmentUnknown, reqIn},
		{"management counts as production", envMgmt, testPool, PlacementRequired, NonProdAllowed, envMgmt, reqIn},
		{"preferred mode", envProd, testPool, PlacementPreferred, NonProdAllowed, envProd, prefIn},
		{"empty label value is a valid pool", envProd, "example.io/pool=", PlacementRequired, NonProdAllowed, envProd, reqIn},
		{"non-production allowed is not steered", envTest, testPool, PlacementRequired, NonProdAllowed, envTest, ""},
		{"no pool configured", envProd, "", PlacementRequired, NonProdAllowed, envProd, ""},
		{"invalid label value", "not a label!", "", PlacementRequired, NonProdAllowed, EnvironmentUnknown, ""},
		// Non-production kept off the pool.
		{"non-production avoided", envTest, testPool, PlacementRequired, NonProdAvoided, envTest, prefNotIn},
		{"non-production forbidden", envTest, testPool, PlacementRequired, NonProdForbidden, envTest, reqNotIn},
		{"forbidden ignores the production mode", "dev", testPool, PlacementPreferred, NonProdForbidden, "dev", reqNotIn},
		{"avoided ignores the production mode", "qa", testPool, PlacementRequired, NonProdAvoided, "qa", prefNotIn},
		{"production unaffected by forbidden", envProd, testPool, PlacementRequired, NonProdForbidden, envProd, reqIn},
		{"unknown stays on the pool under forbidden", "", testPool, PlacementRequired, NonProdForbidden, EnvironmentUnknown, reqIn},
		{"management stays on the pool under forbidden", envMgmt, testPool, PlacementRequired, NonProdForbidden, envMgmt, reqIn},
		{"forbidden without a pool adds nothing", envTest, "", PlacementRequired, NonProdForbidden, envTest, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viper.Set(consts.PROD_POOL_NODE_LABEL, tc.pool)
			viper.Set(consts.PROD_POOL_PLACEMENT, tc.mode)
			viper.Set(consts.NON_PROD_ON_PROD_POOL, tc.nonProd)
			t.Cleanup(viper.Reset)

			vm, err := applyPlacement(newPlacementVM(), tc.env)
			if err != nil {
				t.Fatalf("applyPlacement: %v", err)
			}
			if got := vm.Labels[LabelEnvironment]; got != tc.wantLabel {
				t.Errorf("VM label = %q, want %q", got, tc.wantLabel)
			}
			if got := vm.Spec.Template.ObjectMeta.Labels[LabelEnvironment]; got != tc.wantLabel {
				t.Errorf("template label = %q, want %q", got, tc.wantLabel)
			}
			if got := nodeAffinityForm(vm); got != tc.want {
				t.Errorf("node affinity = %q, want %q", got, tc.want)
			}
		})
	}
}

// nodeAffinityForm says which pool rule the VM carries, as "<form> <operator>"
// such as "required In" or "preferred NotIn"; "" for none and "invalid" when
// it carries anything else.
func nodeAffinityForm(vm *kubevirtv1.VirtualMachine) string {
	aff := vm.Spec.Template.Spec.Affinity
	if aff == nil || aff.NodeAffinity == nil {
		return ""
	}
	req, pref := aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution, aff.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
	switch {
	case req != nil && len(pref) == 0 && len(req.NodeSelectorTerms) == 1:
		if op, ok := poolTermOperator(req.NodeSelectorTerms[0]); ok {
			return PlacementRequired + " " + op
		}
	case req == nil && len(pref) == 1 && pref[0].Weight == 100:
		if op, ok := poolTermOperator(pref[0].Preference); ok {
			return PlacementPreferred + " " + op
		}
	}
	return "invalid"
}

// poolTermOperator returns the operator of a term that names the test pool and
// nothing else.
func poolTermOperator(term corev1.NodeSelectorTerm) (string, bool) {
	if len(term.MatchExpressions) != 1 {
		return "", false
	}
	r := term.MatchExpressions[0]
	if r.Key != "example.io/pool" || len(r.Values) != 1 {
		return "", false
	}
	if r.Operator != corev1.NodeSelectorOpIn && r.Operator != corev1.NodeSelectorOpNotIn {
		return "", false
	}
	return string(r.Operator), true
}

// TestPlacementSettingsValidation pins which settings are refused, both at
// startup and when a VM is built.
func TestPlacementSettingsValidation(t *testing.T) {
	for _, tc := range []struct {
		name, pool, mode, nonProd string
		wantErr                   bool
	}{
		{"unset pool", "", PlacementRequired, NonProdAllowed, false},
		{"valid pool", testPool, PlacementPreferred, NonProdAllowed, false},
		{"no equals sign", "example.io/pool", PlacementRequired, NonProdAllowed, true},
		{"second equals sign", "example.io/pool=prod=blue", PlacementRequired, NonProdAllowed, true},
		{"empty key", "=prod", PlacementRequired, NonProdAllowed, true},
		{"invalid key", "not a key=prod", PlacementRequired, NonProdAllowed, true},
		{"unknown mode", testPool, "sometimes", NonProdAllowed, true},
		{"non-production avoided with a pool", testPool, PlacementRequired, NonProdAvoided, false},
		{"non-production forbidden with a pool", testPool, PlacementRequired, NonProdForbidden, false},
		{"non-production forbidden without a pool", "", PlacementRequired, NonProdForbidden, true},
		{"non-production avoided without a pool", "", PlacementRequired, NonProdAvoided, true},
		{"scheduler word instead of plain word", testPool, PlacementRequired, PlacementRequired, true},
		{"empty non-production setting", testPool, PlacementRequired, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			viper.Set(consts.PROD_POOL_NODE_LABEL, tc.pool)
			viper.Set(consts.PROD_POOL_PLACEMENT, tc.mode)
			viper.Set(consts.NON_PROD_ON_PROD_POOL, tc.nonProd)
			t.Cleanup(viper.Reset)

			if err := ValidatePlacementSettings(); (err != nil) != tc.wantErr {
				t.Fatalf("ValidatePlacementSettings error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}

	// A malformed pool must fail the VM, not create it without the rule.
	viper.Set(consts.PROD_POOL_NODE_LABEL, "example.io/pool=prod=blue")
	t.Cleanup(viper.Reset)
	if _, err := applyPlacement(newPlacementVM(), envProd); err == nil {
		t.Fatal("applyPlacement accepted a malformed pool label")
	}

	// A malformed non-production setting must fail a non-production VM too.
	viper.Set(consts.PROD_POOL_NODE_LABEL, testPool)
	viper.Set(consts.NON_PROD_ON_PROD_POOL, "never")
	if _, err := applyPlacement(newPlacementVM(), envTest); err == nil {
		t.Fatal("applyPlacement accepted a malformed non-production setting")
	}
}

// TestApplyPlacementKeepsPodAntiAffinity: the anti-affinity placement policy
// sets the affinity block first; the pool rule must join it, not replace it.
func TestApplyPlacementKeepsPodAntiAffinity(t *testing.T) {
	viper.Set(consts.PROD_POOL_NODE_LABEL, testPool)
	t.Cleanup(viper.Reset)

	machine := &vitistackv1alpha1.Machine{ObjectMeta: metav1.ObjectMeta{Name: "m", Labels: map[string]string{
		vitistackv1alpha1.ClusterIdAnnotation: "c1", vitistackv1alpha1.NodeRoleAnnotation: "worker",
	}}}
	vm := newPlacementVM()
	vm.Spec.Template.ObjectMeta.Labels = map[string]string{}
	vm, err := applyPlacement(addAntiAffinity(machine, vm), envProd)
	if err != nil {
		t.Fatalf("applyPlacement: %v", err)
	}

	aff := vm.Spec.Template.Spec.Affinity
	if aff == nil || aff.PodAntiAffinity == nil {
		t.Fatalf("pod anti-affinity was dropped: %+v", aff)
	}
	if aff.NodeAffinity == nil {
		t.Fatalf("node affinity missing: %+v", aff)
	}
}
