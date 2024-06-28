package controller

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
				Name: "Kernel_running",
				Help: "Current running Kernels in the cluster",
			},
			[]string{"namespace"},
		),
		KernelCreation: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "Kernel_create_total",
				Help: "Total times of creating Kernels",
			},
			[]string{"namespace"},
		),
		KernelFailCreation: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "Kernel_create_failed_total",
				Help: "Total failure times of creating Kernels",
			},
			[]string{"namespace"},
		),
		KernelCullingCount: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "Kernel_culling_total",
				Help: "Total times of culling Kernels",
			},
			[]string{"namespace", "name"},
		),
		KernelCullingTimestamp: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "last_Kernel_culling_timestamp_seconds",
				Help: "Timestamp of the last Kernel culling in seconds",
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

// scrape gets current running Kernel pods.
func (m *Metrics) scrape() {
	podList := &corev1.PodList{}
	err := m.cli.List(context.TODO(), podList)
	if err != nil {
		return
	}
	podCache := make(map[string]float64)
	for _, v := range podList.Items {
		name, ok := v.ObjectMeta.GetLabels()["Kernel-name"]
		if ok && name == v.Name {
			podCache[v.Namespace] += 1
		}
	}

	for ns, v := range podCache {
		m.runningKernels.WithLabelValues(ns).Set(v)
	}
}
