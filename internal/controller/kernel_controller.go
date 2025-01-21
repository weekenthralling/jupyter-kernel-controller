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

package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/go-logr/logr"
	jupyterorgv1 "github.com/kernel-controller/api/v1"
	"github.com/kernel-controller/internal/metrics"
)

const KernelNameLabel = "jupyter.org/kernel-name"
const KernelIdleLabel = "jupyrator.org/kernel-idle"

const DefaultMonitorContainerImage = "ghcr.io/weekenthralling/kernel-monitor:latest"

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
	Scheme        *runtime.Scheme
	Log           logr.Logger
	Metrics       *metrics.Metrics
	EventRecorder record.EventRecorder
	PrivateKey    string
	PublicKey     string
}

// +kubebuilder:rbac:groups=core,resources=events,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=core,resources=pods,verbs='*'
// +kubebuilder:rbac:groups=jupyter.org,resources=kernels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=jupyter.org,resources=kernels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=jupyter.org,resources=kernels/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// the Kernel object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/reconcile
func (r *KernelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("Kernel", req.NamespacedName)
	log.Info("Reconciliation loop started")

	event := &corev1.Event{}
	getEventErr := r.Get(ctx, req.NamespacedName, event)
	if getEventErr == nil {
		log.Info("Found event for Kernel. Re-emitting...")

		// Find the Kernel that corresponds to the triggered event
		involvedKernel := &jupyterorgv1.Kernel{}
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
	instance := &jupyterorgv1.Kernel{}
	if err := r.Get(ctx, req.NamespacedName, instance); err != nil {
		if ignoreNotFound(err) != nil {
			log.Error(err, "unable to fetch Kernel")
		}
		return ctrl.Result{}, ignoreNotFound(err)
	}

	// Culling kernel if idle for more than the specified time
	if instance.Labels[KernelIdleLabel] == "true" {
		t := time.Now()
		log.Info("Culling idle Kernel", "namespace", instance.Namespace, "name", instance.Name)
		if err := r.Delete(ctx, instance); err != nil {
			log.Error(err, "unable to delete Kernel")
			return ctrl.Result{}, err
		}
		r.Metrics.KernelCullingCount.WithLabelValues(instance.Namespace, instance.Name).Inc()
		r.Metrics.KernelCullingTimestamp.WithLabelValues(instance.Namespace, instance.Name).Set(float64(t.Unix()))
	}

	// Reconcile pod by instance and set reference
	pod := r.generatePod(instance)
	if err := ctrl.SetControllerReference(instance, pod, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	foundPod := &corev1.Pod{}
	err := r.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, foundPod)
	if err != nil && apierrs.IsNotFound(err) {
		log.Info("Creating pod", "namespace", pod.Namespace, "name", pod.Name)
		r.Metrics.KernelCreation.WithLabelValues(pod.Namespace).Inc()
		err = r.Create(ctx, pod)
		if err != nil {
			log.Error(err, "unable to create pod")
			r.Metrics.KernelFailCreation.WithLabelValues(pod.Namespace).Inc()

			r.EventRecorder.Eventf(instance, corev1.EventTypeWarning, "PodCreationFailed", "Failed to create pod %s:%v", pod.Name, err)
			return ctrl.Result{}, err
		}
	} else if err != nil {
		log.Error(err, "error getting pod")
		return ctrl.Result{}, err
	}

	// Update kernel status with pod conditions
	if err := r.updateKernelStatus(instance, foundPod, req); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *KernelReconciler) updateKernelStatus(kernel *jupyterorgv1.Kernel, pod *corev1.Pod, req ctrl.Request) error {

	log := r.Log.WithValues("Kernel", req.NamespacedName)
	ctx := context.Background()

	status := r.createKernelStatus(kernel, pod, req)

	log.Info("Updating Kernel CR Status", "status", status)
	kernel.Status = status
	return r.Status().Update(ctx, kernel)
}

func (r *KernelReconciler) createKernelStatus(kernel *jupyterorgv1.Kernel, pod *corev1.Pod, req ctrl.Request) jupyterorgv1.KernelStatus {
	log := r.Log.WithValues("Kernel", req.NamespacedName)

	// Initialize Kernel CR Status
	log.Info("Initializing Kernel CR Status")
	status := jupyterorgv1.KernelStatus{
		Conditions:     make([]jupyterorgv1.KernelCondition, 0),
		ContainerState: corev1.ContainerState{},
		Phase:          pod.Status.Phase,
		IP:             pod.Status.PodIP,
	}

	// Update the status based on the Pod's status
	if reflect.DeepEqual(pod.Status, corev1.PodStatus{}) {
		log.Info("No pod.Status found. Won't update Kernel conditions and containerState")
		return status
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
	kernelConditions := []jupyterorgv1.KernelCondition{}
	log.Info("Calculating Kernel's Conditions")
	for i := range pod.Status.Conditions {
		condition := PodCondToKernelCond(pod.Status.Conditions[i])
		kernelConditions = append(kernelConditions, condition)
	}

	status.Conditions = kernelConditions

	return status
}

func PodCondToKernelCond(podc corev1.PodCondition) jupyterorgv1.KernelCondition {

	condition := jupyterorgv1.KernelCondition{}

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

// generatePod generate pod from kernel spec template
func (r *KernelReconciler) generatePod(instance *jupyterorgv1.Kernel) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        instance.Name,
			Namespace:   instance.Namespace,
			Labels:      make(map[string]string),
			Annotations: make(map[string]string),
		},
		Spec: *instance.Spec.Template.Spec.DeepCopy(),
	}

	// Copy all the kernel labels to the pod including pod default related labels
	l := &pod.ObjectMeta.Labels
	for k, v := range instance.Labels {
		(*l)[k] = v
	}

	// Copy all the kernel annotations to the pod. excluding kubectl and kernel related annotations
	a := &pod.ObjectMeta.Annotations
	for k, v := range instance.Annotations {
		if !strings.Contains(k, "kubectl") && !strings.Contains(k, "kernel") {
			(*a)[k] = v
		}
	}

	// Set kernel container name
	pod.Spec.Containers[0].Name = instance.Name

	// TODO: Set kernel container command to wait for the kernel monitor to be ready
	pod.Spec.Containers[0].Command = []string{
		"/bin/bash",
		"-c",
		"until (echo -n > /dev/tcp/127.0.0.1/65432) 2>/dev/null; do sleep 1; done; exec /usr/local/bin/bootstrap-kernel.sh",
	}

	// Set Kernel startup envs
	pod.Spec.Containers[0].Env = append(pod.Spec.Containers[0].Env, corev1.EnvVar{
		Name:  "PUBLIC_KEY",
		Value: r.PublicKey,
	}, corev1.EnvVar{
		Name:  "RESPONSE_ADDRESS",
		Value: "127.0.0.1:65432",
	})

	idleTimeout := instance.Spec.IdleTimeoutSeconds
	if idleTimeout != 0 {
		idleTimeout = 3600
	}
	cullingInterval := instance.Spec.CullingIntervalSeconds
	if cullingInterval != 0 {
		cullingInterval = 60
	}

	// Set sidecar container monitoring kernel activity
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{
		Name:  "monitor",
		Image: DefaultMonitorContainerImage,
		Args: []string{
			"--idle-timeout",
			fmt.Sprintf("%d", idleTimeout),
			"--culling-interval",
			fmt.Sprintf("%d", cullingInterval),
			"--private-key",
			r.PrivateKey,
		},
		Env: []corev1.EnvVar{
			{
				Name: "NAME",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.name",
					},
				},
			},
			{
				Name: "NAMESPACE",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "metadata.namespace",
					},
				},
			},
			{
				Name: "IP",
				ValueFrom: &corev1.EnvVarSource{
					FieldRef: &corev1.ObjectFieldSelector{
						FieldPath: "status.podIP",
					},
				},
			},
		},
	})

	pod.Spec.RestartPolicy = corev1.RestartPolicyNever
	return pod
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
		if kernelName, ok := pod.Labels[KernelNameLabel]; ok {
			return kernelName, nil
		}
	}
	return "", fmt.Errorf("object isn't related to a Kernel")
}

// SetupWithManager sets up the controller with the Manager.
func (r *KernelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&jupyterorgv1.Kernel{}).
		Named("kernel").
		Owns(&corev1.Pod{}).
		Complete(r)
}
