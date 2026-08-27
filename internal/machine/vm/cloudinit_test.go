package vm

import (
	"strings"
	"testing"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestRenderNetworkConfigV1_NamStaticFromAllocationResult covers static-via-NAM:
// the NetworkNamespace has type=static but NO spec.ipAllocation.static block.
// static-ip-operator derived the pool and wrote address/subnet/gateway/dns onto
// NetworkConfiguration.status.networkInterfaces[]. Cloud-init must render from
// that allocation result instead of requiring the (absent) static block.
func TestRenderNetworkConfigV1_NamStaticFromAllocationResult(t *testing.T) {
	netNs := &vitistackv1alpha1.NetworkNamespace{
		ObjectMeta: metav1.ObjectMeta{Name: "test-static-networknamespace"},
		Spec: vitistackv1alpha1.NetworkNamespaceSpec{
			IPAllocation: &vitistackv1alpha1.NetworkNamespaceIPAllocation{
				Type: vitistackv1alpha1.IPAllocationTypeStatic,
				// No Static block — NAM-provisioned.
			},
		},
	}
	nc := &vitistackv1alpha1.NetworkConfiguration{
		Spec: vitistackv1alpha1.NetworkConfigurationSpec{
			NetworkInterfaces: []vitistackv1alpha1.NetworkConfigurationInterface{
				{Name: "eth0", MacAddress: "AA:BB:CC:DD:EE:01"},
			},
		},
		Status: vitistackv1alpha1.NetworkConfigurationStatus{
			NetworkInterfaces: []vitistackv1alpha1.NetworkConfigurationInterface{
				{
					MacAddress:    "aa:bb:cc:dd:ee:01",
					IPv4Addresses: []string{"100.64.8.4"},
					IPv4Subnet:    "100.64.8.0/24",
					IPv4Gateway:   "100.64.8.1",
					DNS:           []string{"100.64.8.1"},
				},
			},
		},
	}

	out, err := renderNetworkConfigV1(netNs, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"address: 100.64.8.4",
		"netmask: 255.255.255.0",
		"gateway: 100.64.8.1",
		"dns_nameservers:",
		"- 100.64.8.1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
}

// TestRenderNetworkConfigV1_ManualFallsBackToSpecStatic guards the manual/home
// mode and backward compatibility: when the allocation result on the NC status
// carries only the assigned address (no subnet/gateway/dns), netmask/gateway/dns
// fall back to the hand-written spec.ipAllocation.static block.
func TestRenderNetworkConfigV1_ManualFallsBackToSpecStatic(t *testing.T) {
	netNs := &vitistackv1alpha1.NetworkNamespace{
		ObjectMeta: metav1.ObjectMeta{Name: "manual-nn"},
		Spec: vitistackv1alpha1.NetworkNamespaceSpec{
			IPAllocation: &vitistackv1alpha1.NetworkNamespaceIPAllocation{
				Type: vitistackv1alpha1.IPAllocationTypeStatic,
				Static: &vitistackv1alpha1.StaticIPAllocationConfig{
					IPv4CIDR:    "10.0.1.0/24",
					IPv4Gateway: "10.0.1.1",
					DNS:         []string{"10.0.1.1", "8.8.8.8"},
				},
			},
		},
	}
	nc := &vitistackv1alpha1.NetworkConfiguration{
		Spec: vitistackv1alpha1.NetworkConfigurationSpec{
			NetworkInterfaces: []vitistackv1alpha1.NetworkConfigurationInterface{
				{Name: "eth0", MacAddress: "AA:BB:CC:DD:EE:02"},
			},
		},
		Status: vitistackv1alpha1.NetworkConfigurationStatus{
			NetworkInterfaces: []vitistackv1alpha1.NetworkConfigurationInterface{
				{
					MacAddress:    "aa:bb:cc:dd:ee:02",
					IPv4Addresses: []string{"10.0.1.10"},
					// No IPv4Subnet/IPv4Gateway/DNS — must fall back to spec.static.
				},
			},
		},
	}

	out, err := renderNetworkConfigV1(netNs, nc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"address: 10.0.1.10",
		"netmask: 255.255.255.0",
		"gateway: 10.0.1.1",
		"- 8.8.8.8",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered config missing %q\n---\n%s", want, out)
		}
	}
}
