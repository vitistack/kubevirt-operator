package settings

import (
	"runtime"

	"github.com/spf13/viper"
	"github.com/vitistack/common/pkg/loggers/vlog"
	"github.com/vitistack/common/pkg/settings/dotenv"
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

	viper.SetDefault(consts.DEVELOPMENT, false)
	viper.SetDefault(consts.CPU_MODEL, cpuModel)
	viper.SetDefault(consts.NETWORK_ATTACHMENT_DEFINITION_CNI_VERSION, "1.0.0")
	viper.SetDefault(consts.NAMESPACE, "default")
	viper.SetDefault(consts.LOG_JSON, true)
	viper.SetDefault(consts.LOG_LEVEL, "info")
	viper.SetDefault(consts.MANAGED_BY, "kubevirt-operator")
	viper.SetDefault(consts.VM_NAME_PREFIX, "")
	viper.SetDefault(consts.PVC_VOLUME_MODE, "Block") // Options: "Block" (default) or "Filesystem"
	viper.SetDefault(consts.IP_SOURCE, "vmi")
	viper.SetDefault(consts.KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER, false)
	viper.SetDefault(consts.NAME_MACHINE_PROVIDER, "kubevirt-provider")

	dotenv.LoadDotEnv()

	viper.AutomaticEnv()

	printEnvironmentSettings()
}

func printEnvironmentSettings() {
	settings := []string{
		consts.LOG_JSON,
		consts.LOG_COLORIZE_LINE,
		consts.LOG_ADD_CALLER,
		consts.LOG_DISABLE_STACKTRACE,
		consts.LOG_UNESCAPED_MULTILINE,
		consts.LOG_LEVEL,
	}

	for _, s := range settings {
		val := viper.Get(s)
		if val != nil {
			// #nosec G202
			vlog.Debug(s + "=" + viper.GetString(s))
		}
	}
}
