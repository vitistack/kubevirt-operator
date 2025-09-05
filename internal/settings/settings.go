package settings

import (
	"runtime"

	"github.com/spf13/viper"
	"github.com/vitistack/kubevirt-operator/internal/consts"
)

var (
	Version = "0.0.0"
	Commit  = "localdev"
)

func Init() {
	// Initialize settings here
	// Set CPU model based on architecture
	cpuModel := "host-model" // default for non-ARM architectures
	if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
		cpuModel = "host-passthrough"
	}

	viper.SetDefault(consts.CPU_MODEL, cpuModel)
	viper.SetDefault(consts.JSON_LOGGING, true)

	viper.AutomaticEnv()
}
