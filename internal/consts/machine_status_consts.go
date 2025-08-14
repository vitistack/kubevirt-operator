package consts

// Machine state and phase string constants used across the operator.
// These map directly to values stored in Machine.Status.State and Machine.Status.Phase
// and mirror KubeVirt VMI/VM phases where appropriate.
const (
	MachineStateRunning    = "Running"
	MachineStatePending    = "Pending"
	MachineStateScheduling = "Scheduling"
	MachineStateScheduled  = "Scheduled"
	MachineStateSucceeded  = "Succeeded"
	MachineStateFailed     = "Failed"
	MachineStateUnknown    = "Unknown"
)

const (
	MachinePhaseRunning    = "Running"
	MachinePhasePending    = "Pending"
	MachinePhaseScheduling = "Scheduling"
	MachinePhaseScheduled  = "Scheduled"
	MachinePhaseSucceeded  = "Succeeded"
	MachinePhaseFailed     = "Failed"
	MachinePhaseUnknown    = "Unknown"
)
