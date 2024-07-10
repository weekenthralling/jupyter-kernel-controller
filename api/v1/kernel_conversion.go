package v1

import (
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	klv1beta1 "github.com/jupyter_kernel_controller/api/v1beta1"
)

// ConvertTo converts this Kernel to the Hub version (v1beta1).
func (src *Kernel) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*klv1beta1.Kernel)
	dst.Spec.Template.Spec = src.Spec.Template.Spec
	dst.Status.ReadyReplicas = src.Status.ReadyReplicas
	dst.Status.ContainerState = src.Status.ContainerState
	conditions := []klv1beta1.KernelCondition{}
	for _, c := range src.Status.Conditions {
		newc := klv1beta1.KernelCondition{
			Type:          c.Type,
			LastProbeTime: c.LastProbeTime,
			Reason:        c.Reason,
			Message:       c.Message,
		}
		conditions = append(conditions, newc)
	}
	dst.Status.Conditions = conditions

	return nil
}

/*
ConvertFrom is expected to modify its receiver to contain the converted object.
Most of the conversion is straightforward copying, except for converting our changed field.
*/

// ConvertFrom converts from the Hub version (v1beta1) to this version.
func (dst *Kernel) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*klv1beta1.Kernel)
	dst.Spec.Template.Spec = src.Spec.Template.Spec
	dst.Status.ReadyReplicas = src.Status.ReadyReplicas
	dst.Status.ContainerState = src.Status.ContainerState
	conditions := []KernelCondition{}
	for _, c := range src.Status.Conditions {
		newc := KernelCondition{
			Type:          c.Type,
			LastProbeTime: c.LastProbeTime,
			Reason:        c.Reason,
			Message:       c.Message,
		}
		conditions = append(conditions, newc)
	}
	dst.Status.Conditions = conditions

	return nil
}
