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

	viper.SetDefault(consts.CPU_MODEL, cpuModel)
	viper.SetDefault(consts.LOG_JSON, true)
	viper.SetDefault(consts.LOG_LEVEL, "info")
	viper.SetDefault(consts.MANAGED_BY, "kubevirt-operator")

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
