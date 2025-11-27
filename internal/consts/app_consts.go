package consts

const (
	DEVELOPMENT                = "DEVELOPMENT"
	NAMESPACE                  = "NAMESPACE"
	CPU_MODEL                  = "CPU_MODEL"
	CNI_VERSION                = "CNI_VERSION"
	LOG_LEVEL                  = "LOG_LEVEL"
	LOG_JSON                   = "LOG_JSON"
	LOG_ADD_CALLER             = "LOG_ADD_CALLER"
	LOG_DISABLE_STACKTRACE     = "LOG_DISABLE_STACKTRACE"
	LOG_UNESCAPED_MULTILINE    = "LOG_UNESCAPED_MULTILINE"
	LOG_COLORIZE_LINE          = "LOG_COLORIZE_LINE"
	MANAGED_BY                 = "MANAGED_BY"
	KUBEVIRT_CONFIGS_NAMESPACE = "KUBEVIRT_CONFIGS_NAMESPACE"
	DEFAULT_KUBEVIRT_CONFIG    = "DEFAULT_KUBEVIRT_CONFIG"
	VM_NAME_PREFIX             = "VM_NAME_PREFIX"
	PVC_VOLUME_MODE            = "PVC_VOLUME_MODE"
	// IP_SOURCE controls where public IP addresses are fetched from
	// Valid values: "vmi" (default, from KubeVirt VMI), "networkconfiguration" (from NetworkConfiguration status)
	IP_SOURCE                                    = "IP_SOURCE"
	KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER = "KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER"
	VITISTACK_NAME                               = "VITISTACK_NAME"
	NAME_MACHINE_PROVIDER                        = "NAME_MACHINE_PROVIDER"
)
