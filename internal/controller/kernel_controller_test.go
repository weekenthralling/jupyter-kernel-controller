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
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	v1 "github.com/kernel-controller/api/v1"
)

func TestNameFromInvolvedObject(t *testing.T) {
	testPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "default",
			Labels: map[string]string{
				KernelNameLabel: "foo",
			},
		},
	}

	podEvent := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pod-event",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "Pod",
			Name:      "foo",
			Namespace: "default",
		},
	}

	tests := []struct {
		name         string
		event        *corev1.Event
		expectedName string
	}{
		{
			name:         "pod event",
			event:        podEvent,
			expectedName: "foo",
		},
	}

	objects := []runtime.Object{testPod}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			c := fake.NewFakeClient(objects...)
			kernelName, err := kernelNameFromInvolvedObject(c, &test.event.InvolvedObject)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if kernelName != test.expectedName {
				t.Fatalf("Got %v, Expected %v", kernelName, test.expectedName)
			}
		})
	}
}

func TestCreateKernelStatus(t *testing.T) {
	tests := []struct {
		name           string
		currentKernel  v1.Kernel
		pod            corev1.Pod
		expectedStatus v1.KernelStatus
	}{
		{
			name: "KernelStatusInitialization",
			currentKernel: v1.Kernel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "default",
				},
				Status: v1.KernelStatus{},
			},
			pod: corev1.Pod{},
			expectedStatus: v1.KernelStatus{
				Conditions:     []v1.KernelCondition{},
				ContainerState: corev1.ContainerState{},
			},
		},
		{
			name: "KernelContainerState",
			currentKernel: v1.Kernel{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "default",
				},
				Status: v1.KernelStatus{},
			},
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "default",
				},
				Status: corev1.PodStatus{
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "foo",
							State: corev1.ContainerState{
								Running: &corev1.ContainerStateRunning{
									StartedAt: metav1.Time{},
								},
							},
						},
					},
				},
			},
			expectedStatus: v1.KernelStatus{
				Conditions: []v1.KernelCondition{},
				ContainerState: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{
						StartedAt: metav1.Time{},
					},
				},
			},
		},
		{
			name: "mirroringPodConditions",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "default",
				},
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:               "Running",
							LastProbeTime:      metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
							LastTransitionTime: metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
						},
						{
							Type:               "Waiting",
							LastProbeTime:      metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
							LastTransitionTime: metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
							Reason:             "PodInitializing",
						},
					},
				},
			},
			expectedStatus: v1.KernelStatus{
				Conditions: []v1.KernelCondition{
					{
						Type:               "Running",
						LastProbeTime:      metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
						LastTransitionTime: metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
					},
					{
						Type:               "Waiting",
						LastProbeTime:      metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
						LastTransitionTime: metav1.Date(2024, time.Month(12), 30, 1, 10, 30, 0, time.UTC),
						Reason:             "PodInitializing",
					},
				},
				ContainerState: corev1.ContainerState{},
			},
		},
		{
			name: "unschedulablePod",
			pod: corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "foo",
					Namespace: "default",
				},
				Status: corev1.PodStatus{
					Conditions: []corev1.PodCondition{
						{
							Type:               "PodScheduled",
							LastProbeTime:      metav1.Date(2024, time.Month(4), 21, 1, 10, 30, 0, time.UTC),
							LastTransitionTime: metav1.Date(2024, time.Month(4), 21, 1, 10, 30, 0, time.UTC),
							Message:            "0/1 nodes are available: 1 Insufficient cpu.",
							Status:             "false",
							Reason:             "Unschedulable",
						},
					},
				},
			},
			expectedStatus: v1.KernelStatus{
				Conditions: []v1.KernelCondition{
					{
						Type:               "PodScheduled",
						LastProbeTime:      metav1.Date(2024, time.Month(4), 21, 1, 10, 30, 0, time.UTC),
						LastTransitionTime: metav1.Date(2024, time.Month(4), 21, 1, 10, 30, 0, time.UTC),
						Message:            "0/1 nodes are available: 1 Insufficient cpu.",
						Status:             "false",
						Reason:             "Unschedulable",
					},
				},
				ContainerState: corev1.ContainerState{},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			r := createMockReconciler()
			req := ctrl.Request{}
			status := r.createKernelStatus(&test.currentKernel, &test.pod, req)
			if !reflect.DeepEqual(status, test.expectedStatus) {
				t.Errorf("\nExpect: %v; \nOutput: %v", test.expectedStatus, status)
			}
		})
	}
}

func createMockReconciler() *KernelReconciler {
	return &KernelReconciler{
		Scheme: runtime.NewScheme(),
		Log:    ctrl.Log,
	}
}
