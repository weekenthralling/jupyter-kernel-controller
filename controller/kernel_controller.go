package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jupyter_kernel_controller/api/v1beta1"
	"github.com/jupyter_kernel_controller/reconcilehelper"
	"k8s.io/apimachinery/pkg/util/intstr"

	"k8s.io/client-go/tools/record"

	"github.com/go-logr/logr"
	"github.com/jupyter_kernel_controller/config"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/client/v3/concurrency"

	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	KERNEL_ID_ANNO_NAME         = "jupyter.org/kernel-id"
	KERNEL_CONNECTION_ANNO_NAME = "jupyter.org/kernel-connection-info"
	KERNEL_NAME_LABEL_NAME      = "jupyter.org/kernel-name"
	KERNEL_UPDATED_LABEL_NAME   = "jupyter-kernel-controller/updated"
	KERNEL_UPDATED_LABEL_VALUE  = "True"
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
	EtcdClient    *clientv3.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	Metrics       *Metrics
	EventRecorder record.EventRecorder
}

type KernelConnectionInfo struct {
	ShellPort       uint64 `json:"shell_port"`
	StdinPort       uint64 `json:"stdin_port"`
	IOPubPort       uint64 `json:"iopub_port"`
	ControlPort     uint64 `json:"control_port"`
	HBPort          uint64 `json:"hb_port"`
	IP              string `json:"ip"`
	Key             string `json:"key"`
	Transport       string `json:"transport"`
	SignatureScheme string `json:"signature_scheme"`
	KernelName      string `json:"kernel_name"`
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

	if r.EtcdClient != nil {
		kernelLockKey := instance.Namespace + "/" + instance.Name

		session, err := concurrency.NewSession(r.EtcdClient)
		if err != nil {
			log.Error(err, "unable create etcd distributed lock session")
			return ctrl.Result{}, err
		}
		defer session.Close()

		mutex := concurrency.NewMutex(session, kernelLockKey)

		if err := mutex.Lock(context.Background()); err != nil {
			log.Error(err, fmt.Sprintf("unable lock %s", kernelLockKey))
			return ctrl.Result{}, err
		}

		defer func() {
			if err := mutex.Unlock(context.Background()); err != nil {
				log.Error(err, fmt.Sprintf("unable unlock %s", kernelLockKey))
			}
		}()
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

	// If kernel completed, indicates that the kernel is automatically shut down due to long-term idleness
	// Delete the kernel resource
	if foundPod.Status.Phase == corev1.PodSucceeded {
		log.Info("Culling idle kernel", "namespaces", instance.Namespace, "name", instance.Name)
		if err := r.Delete(ctx, instance, &client.DeleteOptions{}); err != nil {
			log.Error(err, "culling idle kernel error")
			return ctrl.Result{}, err
		}
		t := time.Now()
		r.Metrics.KernelCullingCount.WithLabelValues(foundPod.Namespace, foundPod.Name).Inc()
		r.Metrics.KernelCullingTimestamp.WithLabelValues(foundPod.Namespace, foundPod.Name).Set(float64(t.Unix()))
		return ctrl.Result{}, nil
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

	// Define kernel ports to check and their corresponding values from the config
	setKerneStartupEnvIfNotFound := func(currentKernelEnv map[string]corev1.EnvVar,
		kernelEnv *[]corev1.EnvVar, name string, value string) {
		if _, found := currentKernelEnv[name]; !found {
			*kernelEnv = append(*kernelEnv, corev1.EnvVar{
				Name:  name,
				Value: value,
			})
		}
	}
	cfg := r.Config
	kernelStartupEnv := map[string]string{
		"KERNEL_ID":           uuid.New().String(),
		"KERNEL_SHELL_PORT":   strconv.Itoa(cfg.KernelShellPort),
		"KERNEL_IOPUB_PORT":   strconv.Itoa(cfg.KernelIOPubPort),
		"KERNEL_STDIN_PORT":   strconv.Itoa(cfg.KernelStdinPort),
		"KERNEL_HB_PORT":      strconv.Itoa(cfg.KernelHBPort),
		"KERNEL_CONTROL_PORT": strconv.Itoa(cfg.KernelControlPort),
	}

	kernelEnv := &instance.Spec.Template.Spec.Containers[0].Env
	// Create a map to hold kernel environment variables
	currentKernelEnv := make(map[string]corev1.EnvVar)
	for _, env := range *kernelEnv {
		currentKernelEnv[env.Name] = env
	}

	// Set kernel startup env
	for envName, envValue := range kernelStartupEnv {
		setKerneStartupEnvIfNotFound(currentKernelEnv, kernelEnv, envName, envValue)
	}

	// Set kernel connection info annotation
	annotations, _ := r.createKernelAnnotation(kernelEnv, instance.Name, instance.Namespace)
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

	// Update kernel resource with new env and annotation
	if err := r.Update(ctx, instance); err != nil {
		r.Log.Error(err, "Failed update kernel resource")
		return err
	}

	return nil
}

// createKernelAnnotation create kernel annotation from kernel env
func (r *KernelReconciler) createKernelAnnotation(kernelEnv *[]corev1.EnvVar, name, namespace string) (map[string]string, error) {
	currentKernelEnv := make(map[string]string)
	for _, envItem := range *kernelEnv {
		currentKernelEnv[envItem.Name] = envItem.Value
	}

	// Set kernel id annotation
	kernelAnnotations := make(map[string]string)
	kernelAnnotations[KERNEL_ID_ANNO_NAME] = currentKernelEnv["KERNEL_ID"]

	// Set connection annotation
	formatEnvPort := func(envName string) uint64 {
		port, _ := strconv.ParseUint(currentKernelEnv[envName], 10, 64)
		return port
	}
	kernelConnectionInfo := KernelConnectionInfo{
		ShellPort:       formatEnvPort("KERNEL_SHELL_PORT"),
		IOPubPort:       formatEnvPort("KERNEL_IOPUB_PORT"),
		StdinPort:       formatEnvPort("KERNEL_STDIN_PORT"),
		ControlPort:     formatEnvPort("KERNEL_CONTROL_PORT"),
		HBPort:          formatEnvPort("KERNEL_HB_PORT"),
		IP:              fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace),
		Key:             currentKernelEnv["KERNEL_ID"],
		Transport:       "tcp",
		SignatureScheme: "hmac-sha256",
		KernelName:      "",
	}

	connectionAnnotation, err := json.Marshal(kernelConnectionInfo)
	if err != nil {
		r.Log.Error(err, "Error converting connection label to json")
		return nil, err
	}
	kernelAnnotations[KERNEL_CONNECTION_ANNO_NAME] = string(connectionAnnotation)

	return kernelAnnotations, nil
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

// generateService generate service by kernel
func (r *KernelReconciler) generateService(instance *v1beta1.Kernel, pod *corev1.Pod) *corev1.Service {
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
	addServicePort("KERNEL_SHELL_PORT", "shell-port")
	addServicePort("KERNEL_IOPUB_PORT", "iopub-port")
	addServicePort("KERNEL_STDIN_PORT", "stdin-port")
	addServicePort("KERNEL_HB_PORT", "hb-port")
	addServicePort("KERNEL_CONTROL_PORT", "control-port")

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      instance.Name,
			Namespace: instance.Namespace,
		},
		Spec: corev1.ServiceSpec{
			Type:     "ClusterIP",
			Selector: map[string]string{KERNEL_NAME_LABEL_NAME: instance.Name},
			Ports:    servicePort,
		},
	}
	return svc
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
