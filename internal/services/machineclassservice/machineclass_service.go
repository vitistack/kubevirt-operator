/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package machineclassservice provides functionality for managing MachineClass lookups
// and resource requirement calculations.
package machineclassservice

import (
	"context"
	"fmt"
	"math"

	vitistackv1alpha1 "github.com/vitistack/common/pkg/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResourceRequirements holds the computed resource requirements for a VM
type ResourceRequirements struct {
	Memory  string
	Cores   uint32
	Sockets uint32
	Threads uint32
}

// DefaultResourceRequirements is used as fallback when no MachineClass is specified
var DefaultResourceRequirements = ResourceRequirements{
	Memory:  "2Gi",
	Cores:   2,
	Sockets: 1,
	Threads: 1,
}

// MachineClassService handles MachineClass lookups and resource calculations
type MachineClassService struct {
	client client.Client
}

// NewMachineClassService creates a new MachineClassService
func NewMachineClassService(c client.Client) *MachineClassService {
	return &MachineClassService{
		client: c,
	}
}

// GetMachineClass fetches the MachineClass from the cluster by name.
// Returns the MachineClass if found and enabled, otherwise returns an error.
func (s *MachineClassService) GetMachineClass(ctx context.Context, machineClassName string) (*vitistackv1alpha1.MachineClass, error) {
	if machineClassName == "" {
		return nil, fmt.Errorf("machineClass name is empty")
	}

	machineClass := &vitistackv1alpha1.MachineClass{}
	// MachineClass is cluster-scoped, so no namespace is needed
	err := s.client.Get(ctx, client.ObjectKey{Name: machineClassName}, machineClass)
	if err != nil {
		return nil, fmt.Errorf("failed to get MachineClass '%s': %w", machineClassName, err)
	}

	if !machineClass.Spec.Enabled {
		return nil, fmt.Errorf("MachineClass '%s' is not enabled", machineClassName)
	}

	return machineClass, nil
}

// CalculateResourceRequirements determines CPU and memory requirements based on machine spec and MachineClass
func (s *MachineClassService) CalculateResourceRequirements(ctx context.Context, machine *vitistackv1alpha1.Machine) (*ResourceRequirements, error) {
	// Start with default values
	req := &ResourceRequirements{
		Memory:  DefaultResourceRequirements.Memory,
		Cores:   DefaultResourceRequirements.Cores,
		Sockets: DefaultResourceRequirements.Sockets,
		Threads: DefaultResourceRequirements.Threads,
	}

	// Try to get MachineClass from supervisor cluster
	if machine.Spec.MachineClass != "" {
		machineClass, err := s.GetMachineClass(ctx, machine.Spec.MachineClass)
		if err != nil {
			return nil, err
		}

		// Use MachineClass values
		req.Memory = machineClass.Spec.Memory.Quantity.String()
		req.Cores = safeUintToUint32(machineClass.Spec.CPU.Cores, DefaultResourceRequirements.Cores)
		if machineClass.Spec.CPU.Sockets > 0 {
			req.Sockets = safeUintToUint32(machineClass.Spec.CPU.Sockets, DefaultResourceRequirements.Sockets)
		}
		if machineClass.Spec.CPU.Threads > 0 {
			req.Threads = safeUintToUint32(machineClass.Spec.CPU.Threads, DefaultResourceRequirements.Threads)
		}
	}

	// Override with spec values if provided (allows custom overrides per machine)
	if machine.Spec.Memory > 0 {
		req.Memory = fmt.Sprintf("%dMi", machine.Spec.Memory/1024/1024) // Convert bytes to MiB
	}
	if machine.Spec.CPU.Cores > 0 {
		req.Cores = safeIntToUint32(machine.Spec.CPU.Cores, req.Cores)
	}
	if machine.Spec.CPU.Sockets > 0 {
		req.Sockets = safeIntToUint32(machine.Spec.CPU.Sockets, req.Sockets)
	}
	if machine.Spec.CPU.ThreadsPerCore > 0 {
		req.Threads = safeIntToUint32(machine.Spec.CPU.ThreadsPerCore, req.Threads)
	}

	return req, nil
}

// safeIntToUint32 safely converts an int to uint32, returning the default value
// if the input is negative or exceeds uint32 max value
func safeIntToUint32(val int, defaultVal uint32) uint32 {
	if val <= 0 || val > math.MaxUint32 {
		return defaultVal
	}
	return uint32(val) // #nosec G115 -- bounds checked above
}

// safeUintToUint32 safely converts a uint to uint32, returning the default value
// if the input exceeds uint32 max value
func safeUintToUint32(val uint, defaultVal uint32) uint32 {
	if val > math.MaxUint32 {
		return defaultVal
	}
	return uint32(val) // #nosec G115 -- bounds checked above
}
