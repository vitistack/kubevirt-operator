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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Machine is the Schema for the machines API
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=machines,scope=Namespaced
type Machine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MachineSpec   `json:"spec,omitempty"`
	Status MachineStatus `json:"status,omitempty"`
}

// MachineList contains a list of Machine
// +kubebuilder:object:root=true
type MachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Machine `json:"items"`
}

// MachineSpec defines the desired state of Machine
type MachineSpec struct {
	// The name of the machine
	Name string `json:"name"`
	// The type of the machine
	Type string `json:"type"`
}

// MachineStatus defines the observed state of Machine
type MachineStatus struct {
	// The unique identifier for the machine
	MachineID string `json:"machineID,omitempty"`

	// systemd identifier for the machine
	SystemdID string `json:"systemdID,omitempty"`

	// The current state of the machine
	State string `json:"state,omitempty"`

	// The current status of the machine
	Status string `json:"status,omitempty"`

	// The last time the machine status was updated
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// The provider of the machine
	Provider string `json:"provider,omitempty"`

	// The region where the machine is located
	Region string `json:"region,omitempty"`

	// The zone where the machine is located
	Zone string `json:"zone,omitempty"`

	// The IP address of the machine
	IPAddress []string `json:"ipAddress,omitempty"`

	// IPv6 addresses of the machine
	Ipv6Address []string `json:"ipv6Address,omitempty"`

	// The machine's cpu architecture
	Architecture string `json:"architecture,omitempty"`

	// The machine's operating system
	OperatingSystem string `json:"operatingSystem,omitempty"`

	// The machine's operating system version
	OperatingSystemVersion string `json:"operatingSystemVersion,omitempty"`

	// The machine's kernel version
	KernelVersion string `json:"kernelVersion,omitempty"`

	// The machine's hostname
	Hostname string `json:"hostname,omitempty"`

	// The machine's cpus
	CPUs int `json:"cpus,omitempty"`

	// The machine's memory in bytes
	Memory int64 `json:"memory,omitempty"`

	// The machine's disk size in bytes
	Disks []MachineDisk `json:"disks,omitempty"`
}

type MachineDisk struct {
	// The disk's name
	Name string `json:"name"`
	// The disk's size in bytes
	Size int64 `json:"size"`
	// The disk's type (e.g., SSD, HDD)
	Type string `json:"type"`
	// The disk's mount point
	MountPoint string `json:"mountPoint,omitempty"`
	// The disk's filesystem type (e.g., ext4, xfs)
	FilesystemType string `json:"filesystemType,omitempty"`
	// The disk's UUID
	UUID string `json:"uuid,omitempty"`
	// The disk's label
	Label string `json:"label,omitempty"`
	// The disk's serial number
	SerialNumber string `json:"serialNumber,omitempty"`
}

func init() {
	SchemeBuilder.Register(&Machine{}, &MachineList{})
}
