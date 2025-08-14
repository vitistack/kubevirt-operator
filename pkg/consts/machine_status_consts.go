package consts

// Machine state and phase string constants used across the operator.
// Exported from pkg so external consumers (and tests) can rely on them.
const (
	MachineStateRunning    = "Running"
	MachineStatePending    = "Pending"
	MachineStateScheduling = "Scheduling"
	MachineStateScheduled  = "Scheduled"
	MachineStateSucceeded  = "Succeeded"
	MachineStateFailed     = "Failed"
	MachineStateUnknown    = "Unknown"
)

// Phases mirror states but kept distinct for semantic clarity and future divergence.
const (
	MachinePhaseRunning    = "Running"
	MachinePhasePending    = "Pending"
	MachinePhaseScheduling = "Scheduling"
	MachinePhaseScheduled  = "Scheduled"
	MachinePhaseSucceeded  = "Succeeded"
	MachinePhaseFailed     = "Failed"
	MachinePhaseUnknown    = "Unknown"
)
