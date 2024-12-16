package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jupyter_kernel_controller/api/v1beta1"

	"k8s.io/client-go/tools/record"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	KERNEL_ID_ANNO_NAME        = "jupyter.org/kernel-id"
	KERNEL_NAME_LABEL_NAME     = "jupyter.org/kernel-name"
	KERNEL_UPDATED_LABEL_NAME  = "jupyter-kernel-controller/updated"
	KERNEL_UPDATED_LABEL_VALUE = "True"

	KERNEL_CULLING_ANNO_NAME = "jupyter.org/kernel-deletion"

	DEFAULT_RESTART_POLICY = "Never"
)

/*
We generally want to ignore (not requeue) NotFound errors, since we'll get a
reconciliation request once the object exists, and requeuing in the meantime
won't help.
*/
func ignoreNotFound(err error) error {
	if apierrs.IsNotFound(err) {
		return nil
	}
	return err
}

// KernelReconciler reconciles a Kernel object
type KernelReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	Metrics       *Metrics
	EventRecorder record.EventRecorder
}

// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=core,resources=services,verbs="*"
// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=jupyter.org,resources=kernels;kernels/status;kernels/finalizers,verbs="*"

func (r *KernelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("Kernel", req.NamespacedName)
	log.Info("Reconciliation loop started")

	event := &corev1.Event{}
	getEventErr := r.Get(ctx, req.NamespacedName, event)
	if getEventErr == nil {
		log.Info("Found event for Kernel. Re-emitting...")

		// Find the Kernel that corresponds to the triggered event
		involvedKernel := &v1beta1.Kernel{}
		kernelName, err := kernelNameFromInvolvedObject(r.Client, &event.InvolvedObject)
		if err != nil {
			return ctrl.Result{}, err
		}

		involvedKernelKey := types.NamespacedName{Name: kernelName, Namespace: req.Namespace}
		if err := r.Get(ctx, involvedKernelKey, involvedKernel); err != nil {
			log.Error(err, "unable to fetch Kernel by looking at event")
			return ctrl.Result{}, ignoreNotFound(err)
		}

		// re-emit the event in the Kernel CR
		log.Info("Emitting Kernel Event.", "Event", event)
		r.EventRecorder.Eventf(involvedKernel, event.Type, event.Reason,
			"Reissued from %s/%s: %s", strings.ToLower(event.InvolvedObject.Kind), event.InvolvedObject.Name, event.Message)
		return ctrl.Result{}, nil
	}

	if !apierrs.IsNotFound(getEventErr) {
		return ctrl.Result{}, getEventErr
	}

	// If not found, continue. Is not an event.
	instance := &v1beta1.Kernel{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if ignoreNotFound(err) != nil {
			log.Error(err, "unable to fetch Kernel")
		}
		return ctrl.Result{}, ignoreNotFound(err)
	}

	// Set annotations from kernel resource env
	if err := r.updateKernelResource(instance); err != nil {
		return ctrl.Result{}, err
	}

	// Reconcile pod by instance and set reference
	pod := r.generatePodResource(instance)
	if err := ctrl.SetControllerReference(instance, pod, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	// check if the pod already exists
	foundPod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, foundPod)
	if err != nil && apierrs.IsNotFound(err) {
		log.Info("Creating pod", "namespace", pod.Namespace, "name", pod.Name)
		r.Metrics.KernelCreation.WithLabelValues(pod.Namespace).Inc()
		err = r.Create(ctx, pod)
		if err != nil {
			log.Error(err, "unable to create pod")
			r.Metrics.KernelFailCreation.WithLabelValues(pod.Namespace).Inc()
			return ctrl.Result{}, err
		}
	} else if err != nil {
		log.Error(err, "error getting pod")
		return ctrl.Result{}, err
	}

	// Clean up kernel if annotation `jupyter.org/kernel-deletion=True`
	if instance.Annotations != nil &&
		instance.Annotations[KERNEL_CULLING_ANNO_NAME] == "True" {
		log.Info("Culling idle kernel", "namespace", instance.Namespace, "name", instance.Name)
		if err := r.Delete(ctx, instance, &client.DeleteOptions{}); err != nil {
			log.Error(err, "culling idle kernel error")
			return ctrl.Result{}, err
		}
		t := time.Now()
		r.Metrics.KernelCullingCount.WithLabelValues(foundPod.Namespace, foundPod.Name).Inc()
		r.Metrics.KernelCullingTimestamp.WithLabelValues(foundPod.Namespace, foundPod.Name).Set(float64(t.Unix()))
		return ctrl.Result{}, nil
	}

	// Update kernel status with pod conditions
	if err := r.updateKernelStatus(instance, foundPod, req); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// updateKernelResource set kernel startup env and connection annotation
func (r *KernelReconciler) updateKernelResource(instance *v1beta1.Kernel) error {

	// If kernel updated by controller, skip
	if instance.Labels != nil &&
		instance.Labels[KERNEL_UPDATED_LABEL_NAME] == KERNEL_UPDATED_LABEL_VALUE {
		return nil
	}

	ctx := context.Background()

	kernelEnv := &instance.Spec.Template.Spec.Containers[0].Env

	annotations := r.createKernelAnnotation(kernelEnv)
	if instance.Annotations == nil {
		instance.Annotations = annotations
	} else {
		for k, v := range annotations {
			instance.Annotations[k] = v
		}
	}

	// Set kernel labels 'jupyter-kernel-controller/updated=True'
	if instance.Labels == nil {
		instance.Labels = map[string]string{
			KERNEL_UPDATED_LABEL_NAME: KERNEL_UPDATED_LABEL_VALUE,
		}
	} else {
		instance.Labels[KERNEL_UPDATED_LABEL_NAME] = KERNEL_UPDATED_LABEL_VALUE
	}

	// Set default restart policy
	if len(instance.Spec.Template.Spec.RestartPolicy) <= 0 {
		instance.Spec.Template.Spec.RestartPolicy = DEFAULT_RESTART_POLICY
	}

	// Update kernel resource with new env and annotation
	if err := r.Update(ctx, instance); err != nil {
		r.Log.Error(err, "Failed update kernel resource")
		return err
	}

	return nil
}

// createKernelAnnotation create kernel annotation by kernel env
func (r *KernelReconciler) createKernelAnnotation(kernelEnv *[]corev1.EnvVar) map[string]string {
	currentKernelEnv := make(map[string]string)
	for _, envItem := range *kernelEnv {
		currentKernelEnv[envItem.Name] = envItem.Value
	}

	// Set kernel id annotation
	kernelAnnotations := make(map[string]string)
	kernelAnnotations[KERNEL_ID_ANNO_NAME] = currentKernelEnv["KERNEL_ID"]

	return kernelAnnotations
}

// generatePod generate pod from kernel spec template
func (r *KernelReconciler) generatePodResource(instance *v1beta1.Kernel) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				KERNEL_NAME_LABEL_NAME: instance.Name,
			},
			Annotations: make(map[string]string),
		},
		Spec: *instance.Spec.Template.Spec.DeepCopy(),
	}

	// Copy all the kernel labels to the pod including poddefault related labels
	l := &pod.ObjectMeta.Labels
	for k, v := range instance.Labels {
		(*l)[k] = v
	}

	// Copy all the kernel annotations to the pod.
	a := &pod.ObjectMeta.Annotations
	for k, v := range instance.Annotations {
		if !strings.Contains(k, "kubectl") && !strings.Contains(k, "kernel") {
			(*a)[k] = v
		}
	}

	// Set kernel container name
	pod.Spec.Containers[0].Name = instance.Name
	return pod
}

func (r *KernelReconciler) updateKernelStatus(kernel *v1beta1.Kernel, pod *corev1.Pod, req ctrl.Request) error {

	log := r.Log.WithValues("Kernel", req.NamespacedName)
	ctx := context.Background()

	status, err := r.createKernelStatus(kernel, pod, req)
	if err != nil {
		return err
	}

	log.Info("Updating Kernel CR Status", "status", status)
	kernel.Status = status
	return r.Status().Update(ctx, kernel)
}

func (r *KernelReconciler) createKernelStatus(kernel *v1beta1.Kernel, pod *corev1.Pod, req ctrl.Request) (v1beta1.KernelStatus, error) {

	log := r.Log.WithValues("Kernel", req.NamespacedName)

	// Initialize Kernel CR Status
	log.Info("Initializing Kernel CR Status")
	status := v1beta1.KernelStatus{
		Conditions:     make([]v1beta1.KernelCondition, 0),
		ReadyReplicas:  1,
		ContainerState: corev1.ContainerState{},
	}

	// Update the status based on the Pod's status
	if reflect.DeepEqual(pod.Status, corev1.PodStatus{}) {
		log.Info("No pod.Status found. Won't update Kernel conditions and containerState")
		return status, nil
	}

	// Update status of the CR using the ContainerState of
	// the container that has the same name as the CR.
	// If no container of same name is found, the state of the CR is not updated.
	KernelContainerFound := false
	log.Info("Calculating Kernel's  containerState")
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name != kernel.Name {
			continue
		}

		if pod.Status.ContainerStatuses[i].State == kernel.Status.ContainerState {
			continue
		}

		// Update Kernel CR's status.ContainerState
		cs := pod.Status.ContainerStatuses[i].State
		log.Info("Updating Kernel CR state: ", "state", cs)

		status.ContainerState = cs
		KernelContainerFound = true
		break
	}

	if !KernelContainerFound {
		log.Error(nil, "Could not find container with the same name as Kernel "+
			"in containerStates of Pod. Will not update Kernel's "+
			"status.containerState ")
	}

	// Mirroring pod condition
	var KernelConditions []v1beta1.KernelCondition
	log.Info("Calculating Kernel's Conditions")
	for i := range pod.Status.Conditions {
		condition := PodCondToKernelCond(pod.Status.Conditions[i])
		KernelConditions = append(KernelConditions, condition)
	}

	status.Conditions = KernelConditions

	return status, nil
}

func PodCondToKernelCond(podc corev1.PodCondition) v1beta1.KernelCondition {

	condition := v1beta1.KernelCondition{}

	if len(podc.Type) > 0 {
		condition.Type = string(podc.Type)
	}

	if len(podc.Status) > 0 {
		condition.Status = string(podc.Status)
	}

	if len(podc.Message) > 0 {
		condition.Message = podc.Message
	}

	if len(podc.Reason) > 0 {
		condition.Reason = podc.Reason
	}

	// check if podc.LastProbeTime is null. If so initialize
	// the field with metav1.Now()
	check := podc.LastProbeTime.Time.Equal(time.Time{})
	if !check {
		condition.LastProbeTime = podc.LastProbeTime
	} else {
		condition.LastProbeTime = metav1.Now()
	}

	// check if podc.LastTransitionTime is null. If so initialize
	// the field with metav1.Now()
	check = podc.LastTransitionTime.Time.Equal(time.Time{})
	if !check {
		condition.LastTransitionTime = podc.LastTransitionTime
	} else {
		condition.LastTransitionTime = metav1.Now()
	}

	return condition
}

func kernelNameFromInvolvedObject(c client.Client, object *corev1.ObjectReference) (string, error) {
	name, namespace := object.Name, object.Namespace

	if object.Kind == "Pod" {
		pod := &corev1.Pod{}
		err := c.Get(
			context.TODO(),
			types.NamespacedName{
				Namespace: namespace,
				Name:      name,
			},
			pod,
		)
		if err != nil {
			return "", err
		}
		if kernelName, ok := pod.Labels[KERNEL_NAME_LABEL_NAME]; ok {
			return kernelName, nil
		}
	}
	return "", fmt.Errorf("object isn't related to a Kernel")
}

// SetupWithManager sets up the controller with the Manager.
func (r *KernelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1beta1.Kernel{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}

func CopyPodFields(from, to *corev1.Pod) bool {
	requireUpdate := false
	for k, v := range to.Labels {
		if from.Labels[k] != v {
			requireUpdate = true
		}
	}
	to.Labels = from.Labels

	for k, v := range to.Annotations {
		if from.Annotations[k] != v {
			requireUpdate = true
		}
	}
	to.Annotations = from.Annotations

	if !reflect.DeepEqual(to.Spec, from.Spec) {
		requireUpdate = true
	}
	to.Spec = from.Spec

	return requireUpdate
}
