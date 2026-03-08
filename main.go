package main

import (
	"os"

	ctrl "github.com/mccormickt/cloudflare-tunnel-controller/internal/controller"

	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const defaultNamespace = "default"

var scheme = runtime.NewScheme()

func init() {
	log.SetLogger(zap.New())
	clientgoscheme.AddToScheme(scheme)
	gwapiv1.AddToScheme(scheme)
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
	err = ctrl.NewGatewayAPIController(mgr, defaultNamespace)
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
