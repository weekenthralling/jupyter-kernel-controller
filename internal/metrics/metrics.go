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

package metrics

import (
	"context"

	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Metrics includes metrics used in kernel controller
type Metrics struct {
	cli                    client.Client
	runningKernels         *prometheus.GaugeVec
	KernelCreation         *prometheus.CounterVec
	KernelFailCreation     *prometheus.CounterVec
	KernelCullingCount     *prometheus.CounterVec
	KernelCullingTimestamp *prometheus.GaugeVec
}

func NewMetrics(cli client.Client) *Metrics {
	m := &Metrics{
		cli: cli,
		runningKernels: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "kernel_running",
				Help: "Current running kernels in the cluster",
			},
			[]string{"namespace"},
		),
		KernelCreation: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kernel_create_total",
				Help: "Total times of creating kernels",
			},
			[]string{"namespace"},
		),
		KernelFailCreation: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kernel_create_failed_total",
				Help: "Total failure times of creating kernels",
			},
			[]string{"namespace"},
		),
		KernelCullingCount: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kernel_culling_total",
				Help: "Total times of culling kernels",
			},
			[]string{"namespace", "name"},
		),
		KernelCullingTimestamp: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "last_kernel_culling_timestamp_seconds",
				Help: "Timestamp of the last kernel culling in seconds",
			},
			[]string{"namespace", "name"},
		),
	}

	metrics.Registry.MustRegister(m)
	return m
}

// Describe implements the prometheus.Collector interface.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	m.runningKernels.Describe(ch)
	m.KernelCreation.Describe(ch)
	m.KernelFailCreation.Describe(ch)
}

// Collect implements the prometheus.Collector interface.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.scrape()
	m.runningKernels.Collect(ch)
	m.KernelCreation.Collect(ch)
	m.KernelFailCreation.Collect(ch)
}

// scrape gets current running kernel.
func (m *Metrics) scrape() {
	podList := &corev1.PodList{}
	err := m.cli.List(context.TODO(), podList)
	if err != nil {
		return
	}
	stsCache := make(map[string]float64)
	for _, v := range podList.Items {
		name, ok := v.ObjectMeta.GetLabels()["jupyrator.org/Kernel-name"]
		if ok && name == v.Name {
			stsCache[v.Namespace] += 1
		}
	}

	for ns, v := range stsCache {
		m.runningKernels.WithLabelValues(ns).Set(v)
	}
}
