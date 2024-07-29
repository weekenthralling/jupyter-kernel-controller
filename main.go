/*
Copyright 2024 The Controller Authors.

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

package main

import (
	"flag"
	"os"

	krlv1 "github.com/jupyter_kernel_controller/api/v1"
	krlv1alpha1 "github.com/jupyter_kernel_controller/api/v1alpha1"
	krlv1beta1 "github.com/jupyter_kernel_controller/api/v1beta1"
	"github.com/jupyter_kernel_controller/config"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/jupyter_kernel_controller/controller"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(krlv1.AddToScheme(scheme))
	utilruntime.Must(krlv1alpha1.AddToScheme(scheme))
	utilruntime.Must(krlv1beta1.AddToScheme(scheme))
}

func main() {
	var metricsAddr, leaderElectionNamespace string
	var enableLeaderElection bool
	var probeAddr string

	var Burst int
	var QPS int

	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "probe-addr", ":8081", "The address the health endpoint binds to.")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"Determines the namespace in which the leader election configmap will be created.")

	flag.BoolVar(&enableLeaderElection, "enable-leader-election", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.IntVar(&Burst, "burst", 0, "If it's zero, the created RESTClient will use DefaultBurst")
	flag.IntVar(&QPS, "qps", 0, "If it's zero, the created RESTClient will use DefaultQPS")
	opts := zap.Options{
		Development: true,
	}

	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	cfg := ctrl.GetConfigOrDie()
	if Burst != 0 {
		cfg.Burst = Burst
	}
	if QPS != 0 {
		cfg.QPS = float32(QPS)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionNamespace: leaderElectionNamespace,
		LeaderElectionID:        "kernel-controller",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controller.KernelReconciler{
		Client:        mgr.GetClient(),
		Config:        config.LoadConfig(),
		Log:           ctrl.Log.WithName("controllers").WithName("Kernel"),
		Scheme:        mgr.GetScheme(),
		Metrics:       controller.NewMetrics(mgr.GetClient()),
		EventRecorder: mgr.GetEventRecorderFor("kernel-controller"),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "Kernel")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
