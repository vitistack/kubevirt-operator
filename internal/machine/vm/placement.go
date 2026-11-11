package vm

import (
	"context"
	"strings"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// LabelEnvironment is set on every VM and its template with the environment
// of the cluster the machine belongs to, or EnvironmentUnknown.
const LabelEnvironment = "vitistack.io/environment"

// EnvironmentUnknown labels a VM whose cluster environment could not be read.
const EnvironmentUnknown = "unknown"

const (
	PlacementRequired  = "required"
	PlacementPreferred = "preferred"
)

const kubernetesClusterKind = "KubernetesCluster"

// machineEnvironment returns the environment declared on the KubernetesCluster
// that owns the machine, lower-cased. It returns "" when the machine has no
// such owner or the cluster cannot be read: the caller treats that as
// production, so a lookup failure never moves a production VM off the pool.
func (m *VMManager) machineEnvironment(ctx context.Context, machine *vitistackv1alpha1.Machine) string {
	logger := log.FromContext(ctx)
	for _, owner := range machine.OwnerReferences {
		if owner.Kind != kubernetesClusterKind {
			continue
		}
		cluster := &vitistackv1alpha1.KubernetesCluster{}
		if err := m.supervisorClient.Get(ctx, types.NamespacedName{Name: owner.Name, Namespace: machine.Namespace}, cluster); err != nil {
			logger.Error(err, "Failed to read the machine's KubernetesCluster; treating it as production", "cluster", owner.Name)
			return ""
		}
		return strings.ToLower(strings.TrimSpace(cluster.Spec.Cluster.Environment))
	}
	logger.Info("Machine has no KubernetesCluster owner; treating it as production", "machine", machine.Name)
	return ""
}

// isProduction says whether env is a production environment. An unknown
// environment counts as production.
func isProduction(env string) bool {
	return env == "" || env == "prod" || env == "production"
}

// applyPlacement labels the VM with its environment and, for a production VM
// on a site with a pool configured, adds node affinity for the pool. It keeps
// any pod affinity already set by the placement policy.
func applyPlacement(ctx context.Context, vm *kubevirtv1.VirtualMachine, env string) *kubevirtv1.VirtualMachine {
	label := env
	if label == "" || len(validation.IsValidLabelValue(label)) > 0 {
		label = EnvironmentUnknown
	}
	if vm.Labels == nil {
		vm.Labels = map[string]string{}
	}
	if vm.Spec.Template.ObjectMeta.Labels == nil {
		vm.Spec.Template.ObjectMeta.Labels = map[string]string{}
	}
	vm.Labels[LabelEnvironment] = label
	vm.Spec.Template.ObjectMeta.Labels[LabelEnvironment] = label

	pool := strings.TrimSpace(viper.GetString(consts.PROD_POOL_NODE_LABEL))
	if pool == "" || !isProduction(env) {
		return vm
	}
	key, value, ok := strings.Cut(pool, "=")
	if !ok || key == "" || value == "" {
		log.FromContext(ctx).Error(nil, "Ignoring PROD_POOL_NODE_LABEL: expected key=value", "value", pool)
		return vm
	}

	term := corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
		{Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{value}},
	}}
	nodeAffinity := &corev1.NodeAffinity{}
	if viper.GetString(consts.PROD_POOL_PLACEMENT) == PlacementPreferred {
		nodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{{Weight: 100, Preference: term}}
	} else {
		nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{term}}
	}
	if vm.Spec.Template.Spec.Affinity == nil {
		vm.Spec.Template.Spec.Affinity = &corev1.Affinity{}
	}
	vm.Spec.Template.Spec.Affinity.NodeAffinity = nodeAffinity
	return vm
}
