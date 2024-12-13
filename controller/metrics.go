package controller

import (
	"context"
	"log/slog"

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
	m.KernelCullingCount.Describe(ch)
	m.KernelCullingTimestamp.Describe(ch)
}

// Collect implements the prometheus.Collector interface.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	m.scrape()
	m.runningKernels.Collect(ch)
	m.KernelCreation.Collect(ch)
	m.KernelFailCreation.Collect(ch)
	m.KernelCullingCount.Collect(ch)
	m.KernelCullingTimestamp.Collect(ch)
}

// scrape gets current running Kernel pods.
func (m *Metrics) scrape() {
	podList := &corev1.PodList{}
	err := m.cli.List(context.TODO(), podList)
	if err != nil {
		slog.Error("Failed to list pods", "error", err)
		return
	}

	if len(podList.Items) == 0 {
		slog.Debug("No pods found in the cluster")
		return
	}

	podCache := make(map[string]float64)
	for _, v := range podList.Items {
		if name, ok := v.ObjectMeta.GetLabels()[KERNEL_NAME_LABEL_NAME]; ok && name == v.Name {
			podCache[v.Namespace] += 1
		}
	}

	// Reset the current metrics to clear previous data
	// TODO: Do we really need to reset?
	m.runningKernels.Reset()
	for ns, count := range podCache {
		slog.Debug("Updating runningKernels metric", "namespace", ns, "count", count)
		m.runningKernels.WithLabelValues(ns).Set(count)
	}
}
