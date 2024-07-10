package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type KernelSpec struct {
	// Template describes the kernels that will be created.
	Template KernelTemplateSpec `json:"template,omitempty"`
}

type KernelTemplateSpec struct {
	Spec corev1.PodSpec `json:"spec,omitempty"`
}

type KernelStatus struct {
	// Conditions is an array of current conditions
	Conditions []KernelCondition `json:"conditions"`
	// ReadyReplicas is the number of Pods created by the kernel controller that have a Ready Condition.
	ReadyReplicas int32 `json:"readyReplicas"`
	// ContainerState is the state of underlying container.
	ContainerState corev1.ContainerState `json:"containerState"`
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

// Kernel is the Schema for the kernels API
type Kernel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   KernelSpec   `json:"spec,omitempty"`
	Status KernelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// KernelList contains a list of kernel
type KernelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Kernel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Kernel{}, &KernelList{})
}
