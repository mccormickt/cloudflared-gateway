package main

import (
	"os"

	cfv1alpha1 "github.com/mccormickt/cloudflare-tunnel-controller/api/v1alpha1"
	ctrl "github.com/mccormickt/cloudflare-tunnel-controller/internal/controller"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gwapiv1beta1 "sigs.k8s.io/gateway-api/apis/v1beta1"
)

var scheme = runtime.NewScheme()

func init() {
	log.SetLogger(zap.New())
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gwapiv1.Install(scheme))
	utilruntime.Must(gwapiv1alpha2.Install(scheme))
	utilruntime.Must(gwapiv1beta1.Install(scheme))
	utilruntime.Must(cfv1alpha1.AddToScheme(scheme))
}

func main() {
	logger := log.Log.WithName(ctrl.ControllerName)

	logger.Info("Setting up manager")
	mgr, err := manager.New(config.GetConfigOrDie(), manager.Options{
		Scheme:                 scheme,
		Logger:                 logger,
		HealthProbeBindAddress: ":8081",
	})
	if err != nil {
		logger.Error(err, "Error creating manager")
		os.Exit(1)
	}

	logger.Info("Setting up controller")
	err = ctrl.NewGatewayAPIController(mgr)
	if err != nil {
		logger.Error(err, "Error creating controller")
		os.Exit(1)
	}
	logger.Info("Created GatewayAPI controller")

	logger.Info("Starting manager")
	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		logger.Error(err, "Error starting manager")
		os.Exit(1)
	}
}
