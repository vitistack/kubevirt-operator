package network

import (
	"context"
	"testing"

	netattdefv1 "github.com/k8snetworkplumbingwg/network-attachment-definition-client/pkg/apis/k8s.cni.cncf.io/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// TestEnsureNAD_AlreadyExistsRace covers the concurrent-create race: multiple
// Machines share one VLAN NAD (e.g. vlan2122) and reconcile in parallel. Each
// does GET (NotFound) then CREATE; all but one get AlreadyExists. The NAD
// existing is the desired state, so ensureNetworkAttachmentDefinition must treat
// it as success rather than failing the whole VM reconcile.
func TestEnsureNAD_AlreadyExistsRace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := netattdefv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add netattdef scheme: %v", err)
	}
	gr := schema.GroupResource{Group: "k8s.cni.cncf.io", Resource: "network-attachment-definitions"}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return apierrors.NewNotFound(gr, key.Name) // NAD not present yet on GET
		},
		Create: func(_ context.Context, _ client.WithWatch, obj client.Object, _ ...client.CreateOption) error {
			return apierrors.NewAlreadyExists(gr, obj.GetName()) // a concurrent reconcile created it first
		},
	}).Build()

	m := &NetworkManager{}
	if err := m.ensureNetworkAttachmentDefinition(context.Background(), "vlan2122", "test001", 2122, cl); err != nil {
		t.Fatalf("expected nil error when the NAD is created concurrently (AlreadyExists), got: %v", err)
	}
}
