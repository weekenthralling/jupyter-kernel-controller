package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"k8s.io/client-go/tools/record"

	"github.com/go-logr/logr"
	"github.com/jupyter_kernel_controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultContainerPort = 8888
const DefaultServingPort = 80
const AnnotationRewriteURI = "kernels.kubeflow.org/http-rewrite-uri"
const AnnotationHeadersRequestSet = "kernels.kubeflow.org/http-headers-request-set"

const PrefixEnvVar = "KERNEL_PREFIX"

// The default fsGroup of PodSecurityContext.
// https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.11/#podsecuritycontext-v1-core
const DefaultFSGroup = int64(100)

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
		involvedKernel := &v1alpha1.Kernel{}
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
	instance := &v1alpha1.Kernel{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		log.Error(err, "unable to fetch Kernel")
		return ctrl.Result{}, ignoreNotFound(err)
	}

	// generate pod
	pod := generatePod(instance)
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

	err = updateKernelStatus(r, instance, foundPod, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func generatePod(instance *v1alpha1.Kernel) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"Kernel-name": instance.Name,
			},
			Annotations: make(map[string]string),
		},
		Spec: *instance.Spec.Template.Spec.DeepCopy(),
	}

	// copy all of the kernel labels to the pod including poddefault related labels
	l := &pod.ObjectMeta.Labels
	for k, v := range instance.ObjectMeta.Labels {
		(*l)[k] = v
	}

	// copy all of the Kernel annotations to the pod.
	a := &pod.ObjectMeta.Annotations
	for k, v := range instance.ObjectMeta.Annotations {
		if !strings.Contains(k, "kubectl") && !strings.Contains(k, "kernel") {
			(*a)[k] = v
		}
	}

	return pod
}

func updateKernelStatus(r *KernelReconciler, kernel *v1alpha1.Kernel, pod *corev1.Pod, req ctrl.Request) error {

	log := r.Log.WithValues("Kernel", req.NamespacedName)
	ctx := context.Background()

	status, err := createKernelStatus(r, kernel, pod, req)
	if err != nil {
		return err
	}

	log.Info("Updating Kernel CR Status", "status", status)
	kernel.Status = status
	return r.Status().Update(ctx, kernel)
}

func createKernelStatus(r *KernelReconciler, kernel *v1alpha1.Kernel, pod *corev1.Pod, req ctrl.Request) (v1alpha1.KernelStatus, error) {

	log := r.Log.WithValues("Kernel", req.NamespacedName)

	// Initialize Kernel CR Status
	log.Info("Initializing Kernel CR Status")
	status := v1alpha1.KernelStatus{
		Conditions:     make([]v1alpha1.KernelCondition, 0),
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
	KernelConditions := []v1alpha1.KernelCondition{}
	log.Info("Calculating Kernel's Conditions")
	for i := range pod.Status.Conditions {
		condition := PodCondToKernelCond(pod.Status.Conditions[i])
		KernelConditions = append(KernelConditions, condition)
	}

	status.Conditions = KernelConditions

	return status, nil
}

func PodCondToKernelCond(podc corev1.PodCondition) v1alpha1.KernelCondition {

	condition := v1alpha1.KernelCondition{}

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
		if kernelName, ok := pod.Labels["kernel-name"]; ok {
			return kernelName, nil
		}
	}
	return "", fmt.Errorf("object isn't related to a Kernel")
}

// SetupWithManager sets up the controller with the Manager.
func (r *KernelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Kernel{}).
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
