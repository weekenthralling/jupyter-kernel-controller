/*
Copyright 2024.

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

package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// KernelSpec defines the desired state of Kernel.
type KernelSpec struct {
	Template corev1.PodTemplateSpec `json:"template"`
	// IdleTimeoutSeconds is the number of seconds of inactivity before a kernel is automatically deleted.
	// If provided, the controller will create a sidecar container to monitor the kernel's activity.
	// +optional
	IdleTimeoutSeconds int32 `json:"idleTimeoutSeconds,omitempty"`
	// CullingIntervalSeconds is the number of seconds between checking for idle kernel. default is 60 seconds.
	CullingIntervalSeconds int32 `json:"cullingIntervalSeconds,omitempty"`
}

// KernelStatus defines the observed state of Kernel.
type KernelStatus struct {
	// Conditions is an array of current conditions
	Conditions []KernelCondition `json:"conditions"`
	// ContainerState is the state of underlying container.
	ContainerState corev1.ContainerState `json:"containerState"`
	Phase          corev1.PodPhase       `json:"phase"`
	// IP is the IP address of the kernelmanager.
	IP string `json:"ip"`
}

type KernelCondition struct {
	// Type is the type of the condition. Possible values are Running|Waiting|Terminated
	Type string `json:"type"`
	// Status is the status of the condition. Can be True, False, Unknown.
	Status string `json:"status"`
	// Last time we probed the condition.
	// +optional
	LastProbeTime metav1.Time `json:"lastProbeTime,omitempty"`
	// Last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
	// (brief) reason the container is in the current state
	// +optional
	Reason string `json:"reason,omitempty"`
	// Message regarding why the container is in the current state.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ADDRESS",type="string",JSONPath=".status.ip",description="The IP address of the kernel"
// +kubebuilder:printcolumn:name="PHASE",type="string",JSONPath=".status.phase",description="The phase of the kernel"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".status.containerState.running.startedAt"

// Kernel is the Schema for the kernels API.
type Kernel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KernelSpec   `json:"spec,omitempty"`
	Status KernelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KernelList contains a list of Kernel.
type KernelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Kernel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Kernel{}, &KernelList{})
}
