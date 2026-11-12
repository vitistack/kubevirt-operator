package vm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"github.com/vitistack/kubevirt-operator/internal/consts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	kubevirtv1 "kubevirt.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// Values of NON_PROD_ON_PROD_POOL.
const (
	NonProdAllowed   = "allowed"
	NonProdAvoided   = "avoided"
	NonProdForbidden = "forbidden"
)

const kubernetesClusterKind = "KubernetesCluster"

// environmentLookupTimeout bounds the read of the owning KubernetesCluster, so
// an unreachable or forbidden API reaches the production fallback.
const environmentLookupTimeout = 10 * time.Second

// SetClusterReader sets the reader used to look up a machine's
// KubernetesCluster. Pass an uncached reader: a cached client would start an
// informer and wait for it to sync.
func (m *VMManager) SetClusterReader(r client.Reader) {
	m.clusterReader = r
}

// prodPool parses PROD_POOL_NODE_LABEL (key=value). ok is false when no pool
// is configured.
func prodPool() (key, value string, ok bool, err error) {
	raw := strings.TrimSpace(viper.GetString(consts.PROD_POOL_NODE_LABEL))
	if raw == "" {
		return "", "", false, nil
	}
	key, value, found := strings.Cut(raw, "=")
	if !found {
		return "", "", false, fmt.Errorf("%s %q: expected key=value", consts.PROD_POOL_NODE_LABEL, raw)
	}
	if errs := validation.IsQualifiedName(key); len(errs) > 0 {
		return "", "", false, fmt.Errorf("%s %q: invalid label key: %s", consts.PROD_POOL_NODE_LABEL, raw, strings.Join(errs, "; "))
	}
	if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
		return "", "", false, fmt.Errorf("%s %q: invalid label value: %s", consts.PROD_POOL_NODE_LABEL, raw, strings.Join(errs, "; "))
	}
	return key, value, true, nil
}

// ValidatePlacementSettings rejects a malformed pool label or placement mode,
// so a typo stops the operator at startup instead of misplacing VMs. Keeping
// non-production VMs off a pool that is not configured is refused too: the
// site would believe its pool is protected while nothing is.
func ValidatePlacementSettings() error {
	_, _, poolSet, err := prodPool()
	if err != nil {
		return err
	}
	switch mode := viper.GetString(consts.PROD_POOL_PLACEMENT); mode {
	case PlacementRequired, PlacementPreferred:
	default:
		return fmt.Errorf("%s %q: expected %q or %q", consts.PROD_POOL_PLACEMENT, mode, PlacementRequired, PlacementPreferred)
	}
	nonProd := viper.GetString(consts.NON_PROD_ON_PROD_POOL)
	if err := validNonProd(nonProd); err != nil {
		return err
	}
	if nonProd != NonProdAllowed && !poolSet {
		return fmt.Errorf("%s %q needs %s to be set", consts.NON_PROD_ON_PROD_POOL, nonProd, consts.PROD_POOL_NODE_LABEL)
	}
	return nil
}

func validNonProd(v string) error {
	switch v {
	case NonProdAllowed, NonProdAvoided, NonProdForbidden:
		return nil
	}
	return fmt.Errorf("%s %q: expected %q, %q or %q", consts.NON_PROD_ON_PROD_POOL, v, NonProdAllowed, NonProdAvoided, NonProdForbidden)
}

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
		reader := m.clusterReader
		if reader == nil {
			reader = m.supervisorClient
		}
		lookupCtx, cancel := context.WithTimeout(ctx, environmentLookupTimeout)
		defer cancel()
		cluster := &vitistackv1alpha1.KubernetesCluster{}
		if err := reader.Get(lookupCtx, types.NamespacedName{Name: owner.Name, Namespace: machine.Namespace}, cluster); err != nil {
			logger.Error(err, "Failed to read the machine's KubernetesCluster; treating it as production", "cluster", owner.Name)
			return ""
		}
		return strings.ToLower(strings.TrimSpace(cluster.Spec.Cluster.Environment))
	}
	logger.Info("Machine has no KubernetesCluster owner; treating it as production", "machine", machine.Name)
	return ""
}

// isProduction says whether env is a production environment. Management
// clusters run production infrastructure, and an unknown environment also
// counts as production.
func isProduction(env string) bool {
	return env == "" || env == "prod" || env == "production" || env == "mgmt"
}

// environmentLabel is the label value for env: "production" is written as
// "prod", so one selector finds every production VM.
func environmentLabel(env string) string {
	if env == "production" {
		return "prod"
	}
	return env
}

// applyPlacement labels the VM with its environment and, on a site with a pool
// configured, adds node affinity: a production VM for the pool, and a
// non-production VM against it when NON_PROD_ON_PROD_POOL says so. It keeps
// any pod affinity already set by the placement policy. It fails on a
// malformed setting rather than creating a VM without its rule.
func applyPlacement(vm *kubevirtv1.VirtualMachine, env string) (*kubevirtv1.VirtualMachine, error) {
	label := environmentLabel(env)
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

	key, value, ok, err := prodPool()
	if err != nil {
		return nil, err
	}
	if !ok {
		return vm, nil
	}

	operator, mode := corev1.NodeSelectorOpIn, viper.GetString(consts.PROD_POOL_PLACEMENT)
	if !isProduction(env) {
		nonProd := viper.GetString(consts.NON_PROD_ON_PROD_POOL)
		if err := validNonProd(nonProd); err != nil {
			return nil, err
		}
		switch nonProd {
		case NonProdAllowed:
			return vm, nil
		case NonProdAvoided:
			operator, mode = corev1.NodeSelectorOpNotIn, PlacementPreferred
		case NonProdForbidden:
			operator, mode = corev1.NodeSelectorOpNotIn, PlacementRequired
		}
	}

	// NotIn also matches hosts without the label, so an unlabelled host stays
	// open to non-production VMs.
	term := corev1.NodeSelectorTerm{MatchExpressions: []corev1.NodeSelectorRequirement{
		{Key: key, Operator: operator, Values: []string{value}},
	}}
	nodeAffinity := &corev1.NodeAffinity{}
	if mode == PlacementPreferred {
		nodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{{Weight: 100, Preference: term}}
	} else {
		nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{term}}
	}
	if vm.Spec.Template.Spec.Affinity == nil {
		vm.Spec.Template.Spec.Affinity = &corev1.Affinity{}
	}
	vm.Spec.Template.Spec.Affinity.NodeAffinity = nodeAffinity
	return vm, nil
}
