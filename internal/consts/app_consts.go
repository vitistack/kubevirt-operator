package consts

const (
	DEVELOPMENT = "DEVELOPMENT"
	NAMESPACE   = "NAMESPACE"
	CPU_MODEL   = "CPU_MODEL"
	// DISABLE_CPU_HT_FEATURE, when true, adds a `disable` policy for the "ht"
	// CPU feature on host-model VMs. Workaround for a libvirt host-model
	// regression (KubeVirt >= v1.8.0) where the expanded host-model CPU requires
	// the "ht" (HTT) CPUID flag that QEMU does not expose for the guest topology,
	// making check='full' validation fail ("guest CPU doesn't match
	// specification: extra features: ht") so the VM never starts. Enabled by
	// default; opt out per cluster by setting it to false.
	DISABLE_CPU_HT_FEATURE                    = "DISABLE_CPU_HT_FEATURE"
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
	// Valid values: "ReadWriteMany" (default), "ReadWriteOnce", "ReadOnlyMany"
	PVC_ACCESS_MODE = "PVC_ACCESS_MODE"
	// STORAGE_CLASS_NAME allows specifying a storage class to use for PVCs
	// If empty (default), the cluster's default storage class will be used
	STORAGE_CLASS_NAME = "STORAGE_CLASS_NAME"
	// IP_SOURCE controls where public IP addresses are fetched from
	// Valid values: "vmi" (default, from KubeVirt VMI), "networkconfiguration" (from NetworkConfiguration status)
	IP_SOURCE                                    = "IP_SOURCE"
	KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER = "KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER"
	VITISTACK_NAME                               = "VITISTACK_NAME"
	NAME_MACHINE_PROVIDER                        = "NAME_MACHINE_PROVIDER"
	// MAX_CONCURRENT_RECONCILES is the maximum number of Machine
	// reconciliations that run in parallel. Default: 5.
	MAX_CONCURRENT_RECONCILES = "MAX_CONCURRENT_RECONCILES"
)
