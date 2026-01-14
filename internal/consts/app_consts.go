package consts

const (
	DEVELOPMENT                               = "DEVELOPMENT"
	NAMESPACE                                 = "NAMESPACE"
	CPU_MODEL                                 = "CPU_MODEL"
	MACHINE_TYPE                              = "MACHINE_TYPE"
	NETWORK_ATTACHMENT_DEFINITION_CNI_VERSION = "NETWORK_ATTACHMENT_DEFINITION_CNI_VERSION"
	LOG_LEVEL                                 = "LOG_LEVEL"
	LOG_JSON                                  = "LOG_JSON"
	LOG_ADD_CALLER                            = "LOG_ADD_CALLER"
	LOG_DISABLE_STACKTRACE                    = "LOG_DISABLE_STACKTRACE"
	LOG_UNESCAPED_MULTILINE                   = "LOG_UNESCAPED_MULTILINE"
	LOG_COLORIZE_LINE                         = "LOG_COLORIZE_LINE"
	MANAGED_BY                                = "MANAGED_BY"
	DEFAULT_KUBEVIRT_CONFIG                   = "DEFAULT_KUBEVIRT_CONFIG"
	VM_NAME_PREFIX                            = "VM_NAME_PREFIX"
	PVC_VOLUME_MODE                           = "PVC_VOLUME_MODE"
	// PVC_ACCESS_MODE controls the access mode for PVCs and DataVolumes
	// Valid values: "ReadWriteOnce" (default), "ReadWriteMany", "ReadOnlyMany"
	PVC_ACCESS_MODE = "PVC_ACCESS_MODE"
	// IP_SOURCE controls where public IP addresses are fetched from
	// Valid values: "vmi" (default, from KubeVirt VMI), "networkconfiguration" (from NetworkConfiguration status)
	IP_SOURCE                                    = "IP_SOURCE"
	KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER = "KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER"
	VITISTACK_NAME                               = "VITISTACK_NAME"
	NAME_MACHINE_PROVIDER                        = "NAME_MACHINE_PROVIDER"

	// Architecture constants
	ArchARM64 = "arm64"
	ArchAMD64 = "amd64"

	// Machine type constants for KubeVirt
	// ARM64 only supports "virt" machine type
	// x86_64/amd64 typically uses "pc-q35" (modern) or "pc-i440fx" (legacy)
	MachineTypeVirt   = "virt"
	MachineTypeQ35    = "q35"
	MachineTypeI440FX = "pc-i440fx"
)
