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
	// STORAGE_CLASS_NAME_ISO overrides the storage class used for boot-ISO
	// volumes (both the shared, version-named PVC and the per-VM ISO
	// DataVolume). Boot ISOs often need a different class than root disks: a
	// shared ISO must be ReadWriteMany, which on some backends (e.g. Ceph) is
	// only available on a different class than the block class used for root
	// disks — CephFS supports RWX+Filesystem whereas Ceph RBD does not.
	// Resolution order: STORAGE_CLASS_NAME_ISO if set, else STORAGE_CLASS_NAME,
	// else the cluster's default storage class.
	STORAGE_CLASS_NAME_ISO = "STORAGE_CLASS_NAME_ISO"
	// SHARED_BOOT_ISO controls whether the boot ISO is provisioned as a single
	// shared, version-named DataVolume/PVC per namespace (mounted read-only by
	// every VM of that version) instead of one per-VM DataVolumeTemplate.
	// Valid values: "true" (default), "false".
	SHARED_BOOT_ISO = "SHARED_BOOT_ISO"
	// SHARED_BOOT_ISO_ACCESS_MODE is the access mode requested for the shared
	// boot-ISO PVC. It must be ReadWriteMany (default) for the volume to be
	// mounted by VMs across multiple nodes simultaneously, which is the whole
	// point of sharing. Set explicitly rather than negotiated from the CDI
	// StorageProfile so a profile that only advertises RWO does not silently
	// downgrade the shared volume to an unsharable RWO PVC. Requires a storage
	// class that supports RWX for the volume mode (e.g. CephFS for Filesystem).
	// Valid values: "ReadWriteMany" (default), "ReadWriteOnce", "ReadOnlyMany".
	SHARED_BOOT_ISO_ACCESS_MODE = "SHARED_BOOT_ISO_ACCESS_MODE"
	// SHARED_ISO_PVC_VOLUME_MODE is the volume mode for the shared boot-ISO PVC.
	// Defaults to "Filesystem", which CDI needs to import the disk.img the CDRom
	// device serves and which virt-launcher needs for its ownership chown. The
	// chosen mode must be compatible with the access mode on the resolved storage
	// class (e.g. CephFS offers RWX only with Filesystem; Ceph RBD offers RWX
	// only with Block). Valid values: "Filesystem" (default), "Block".
	SHARED_ISO_PVC_VOLUME_MODE = "SHARED_ISO_PVC_VOLUME_MODE"
	// IP_SOURCE controls where public IP addresses are fetched from
	// Valid values: "vmi" (default, from KubeVirt VMI), "networkconfiguration" (from NetworkConfiguration status)
	IP_SOURCE                                    = "IP_SOURCE"
	KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER = "KUBEVIRT_SUPPORT_CONTAINERIZED_DATA_IMPORTER"
	VITISTACK_NAME                               = "VITISTACK_NAME"
	NAME_MACHINE_PROVIDER                        = "NAME_MACHINE_PROVIDER"
	// MAX_CONCURRENT_RECONCILES is the maximum number of Machine
	// reconciliations that run in parallel. Default: 5.
	MAX_CONCURRENT_RECONCILES = "MAX_CONCURRENT_RECONCILES"
	// REMOTE_CLIENT_QPS is the client-side throttle (queries per second) for
	// clients talking to remote KubeVirt clusters. client-go defaults to 5,
	// which is low for this controller's per-reconcile call volume. Default: 50.
	REMOTE_CLIENT_QPS = "REMOTE_CLIENT_QPS"
	// REMOTE_CLIENT_BURST is the client-side burst for remote KubeVirt cluster
	// clients. client-go defaults to 10. Default: 100.
	REMOTE_CLIENT_BURST = "REMOTE_CLIENT_BURST"
)
