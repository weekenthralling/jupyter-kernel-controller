package controller

import (
	"context"
	"fmt"
	"github.com/jupyter_kernel_controller/reconcilehelper"
	"k8s.io/apimachinery/pkg/util/intstr"
	"reflect"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/tools/record"

	"github.com/go-logr/logr"
	"github.com/jupyter_kernel_controller/api/v1alpha1"
	"github.com/jupyter_kernel_controller/config"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	Config        *config.Config
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

	// Reconcile pod by instance and set reference
	pod := r.generatePod(instance)
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

	// Reconcile service by instance and set reference
	service := r.generateService(instance, pod)
	if err := ctrl.SetControllerReference(instance, service, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}

	// Check if the Service already exists
	foundService := &corev1.Service{}
	justCreated := false
	err = r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace}, foundService)
	if err != nil && apierrs.IsNotFound(err) {
		log.Info("Creating Service", "namespace", service.Namespace, "name", service.Name)
		err = r.Create(ctx, service)
		justCreated = true
		if err != nil {
			log.Error(err, "unable to create Service")
			return ctrl.Result{}, err
		}
	} else if err != nil {
		log.Error(err, "error getting Service")
		return ctrl.Result{}, err
	}

	// Update the foundService object and write the result back if there are any changes
	if !justCreated && reconcilehelper.CopyServiceFields(service, foundService) {
		log.Info("Updating Service\n", "namespace", service.Namespace, "name", service.Name)
		err = r.Update(ctx, foundService)
		if err != nil {
			log.Error(err, "unable to update Service")
			return ctrl.Result{}, err
		}
	}

	err = updateKernelStatus(r, instance, foundPod, req)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// generatePod generate pod from kernel spec template
func (r *KernelReconciler) generatePod(instance *v1alpha1.Kernel) *corev1.Pod {
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

	// copy all the kernel labels to the pod including poddefault related labels
	l := &pod.ObjectMeta.Labels
	for k, v := range instance.ObjectMeta.Labels {
		(*l)[k] = v
	}

	// copy all the kernel annotations to the pod.
	a := &pod.ObjectMeta.Annotations
	for k, v := range instance.ObjectMeta.Annotations {
		if !strings.Contains(k, "kubectl") && !strings.Contains(k, "kernel") {
			(*a)[k] = v
		}
	}

	// set kernel port env
	addKernelPortEnvIfNotFound(pod, r.Config)
	return pod
}

// generateService generate service by kernel
func (r *KernelReconciler) generateService(instance *v1alpha1.Kernel, pod *corev1.Pod) *corev1.Service {
	var servicePort []corev1.ServicePort
	addServicePort := func(envName, portName string) {
		for _, env := range pod.Spec.Containers[0].Env {
			if env.Name == envName {
				port, _ := strconv.Atoi(env.Value)
				servicePort = append(servicePort, corev1.ServicePort{
					Name:       portName,
					Port:       int32(port),
					Protocol:   "TCP",
					TargetPort: intstr.FromInt32(int32(port)),
				})
			}
		}
	}

	// add service port from pod container env
	addServicePort("SHELL_PORT", "shell-port")
	addServicePort("IOPUB_PORT", "iopub-port")
	addServicePort("STDIN_PORT", "stdin-port")
	addServicePort("HB_PORT", "hb-port")
	addServicePort("CONTROL_PORT", "control-port")

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     "ClusterIP",
			Selector: map[string]string{"Kernel-name": instance.Name},
			Ports:    servicePort,
		},
	}
	return svc
}

// addKernelPortEnvIfNotFound Define a helper function to create an EnvVar and add it to kernelPorts if not found
func addKernelPortEnvIfNotFound(pod *corev1.Pod, config *config.Config) {
	setPortIfNotFound := func(kernelEnvMap map[string]corev1.EnvVar,
		kernelPorts *[]corev1.EnvVar, name string, value int) {
		if _, found := kernelEnvMap[name]; !found {
			*kernelPorts = append(*kernelPorts, corev1.EnvVar{
				Name:  name,
				Value: strconv.Itoa(value),
			})
		}
	}

	kernelEnv := &pod.Spec.Containers[0].Env

	// Create a map to hold kernel environment variables
	kernelEnvMap := make(map[string]corev1.EnvVar)
	for _, env := range *kernelEnv {
		kernelEnvMap[env.Name] = env
	}

	// Define kernel ports to check and their corresponding values from the config
	ports := map[string]int{
		"SHELL_PORT":   config.ShellPort,
		"IOPUB_PORT":   config.IOPubPort,
		"STDIN_PORT":   config.StdinPort,
		"HB_PORT":      config.HBPort,
		"CONTROL_PORT": config.ControlPort,
	}

	// Initialize a slice to hold the new environment variables
	var kernelPorts []corev1.EnvVar

	// Check each port and add it if not found
	for name, value := range ports {
		setPortIfNotFound(kernelEnvMap, &kernelPorts, name, value)
	}

	// If there are new environment variables, append them to the container's environment variables
	if len(kernelPorts) > 0 {
		*kernelEnv = append(*kernelEnv, kernelPorts...)
	}
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
	var KernelConditions []v1alpha1.KernelCondition
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
