package services

import (
	"github.com/vitistack/kubevirt-operator/internal/services/kubevirtconfigservice"
	"github.com/vitistack/kubevirt-operator/internal/services/machineproviderservice"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	MachineProviderService machineproviderservice.MachineProviderService
	KubevirtConfigService  kubevirtconfigservice.KubevirtConfigService
)

func InitializeServices(c client.Client) {
	MachineProviderService = *machineproviderservice.NewMachineProviderService(c)
	KubevirtConfigService = *kubevirtconfigservice.NewKubevirtConfigService(c)
}
