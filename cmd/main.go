package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"
	"github.com/mccormickt/cloudflared-gateway/internal/cloudflare"
	controller "github.com/mccormickt/cloudflared-gateway/internal/controller"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	apisxv1alpha1 "sigs.k8s.io/gateway-api/apisx/v1alpha1"
	gwapiconsts "sigs.k8s.io/gateway-api/pkg/consts"
)

var scheme = runtime.NewScheme()

// builtAgainstGatewayAPIVersion is the Gateway API bundle version this controller
// is built against, taken from the linked gateway-api module so it cannot drift
// from go.mod. XBackend CRDs must match this GA major.minor release line.
const builtAgainstGatewayAPIVersion = gwapiconsts.BundleVersion

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gwapiv1.Install(scheme))
	utilruntime.Must(apisxv1alpha1.Install(scheme))
	utilruntime.Must(cfv1alpha1.AddToScheme(scheme))
}

// experimentalBackendsEnabled gives an explicit flag precedence over the
// environment and rejects invalid environment values instead of disabling it.
func experimentalBackendsEnabled(fs *flag.FlagSet) (bool, error) {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "enable-experimental-backends" {
			explicit = true
		}
	})
	if explicit {
		return strconv.ParseBool(fs.Lookup("enable-experimental-backends").Value.String())
	}
	v, ok := os.LookupEnv("ENABLE_EXPERIMENTAL_BACKENDS")
	if !ok {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("ENABLE_EXPERIMENTAL_BACKENDS must be a boolean: %w", err)
	}
	return b, nil
}

func main() {
	flag.Bool("enable-experimental-backends", false,
		"Enable support for the experimental Gateway API XBackend resource "+
			"(gateway.networking.x-k8s.io), letting routes target external FQDN destinations.")
	flag.Parse()

	ctrl.SetLogger(zap.New())
	logger := ctrl.Log.WithName(controller.ControllerName)

	enableExperimentalBackends, err := experimentalBackendsEnabled(flag.CommandLine)
	if err != nil {
		logger.Error(err, "Invalid experimental backend configuration")
		os.Exit(1)
	}

	cfClient, err := cloudflare.NewClientFromEnv()
	if err != nil {
		logger.Error(err, "Error creating Cloudflare client")
		os.Exit(1)
	}

	cfg := ctrl.GetConfigOrDie()

	// Fail fast if the Gateway API CRDs this controller needs aren't installed,
	// and check the installed bundle's channel/version for compatibility.
	if err := controller.PreflightCheckCRDs(cfg, enableExperimentalBackends, builtAgainstGatewayAPIVersion, logger); err != nil {
		logger.Error(err, "Gateway API CRD preflight check failed")
		os.Exit(1)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Logger:                 logger,
		HealthProbeBindAddress: ":8081",
		LeaderElection:         true,
		LeaderElectionID:       "cloudflared-gateway.jan0ski.net",
	})
	if err != nil {
		logger.Error(err, "Error creating manager")
		os.Exit(1)
	}

	reconciler := &controller.GatewayReconciler{
		CloudflareClient:     cfClient,
		ControllerName:       gwapiv1.GatewayController(controller.ControllerName),
		ExperimentalBackends: enableExperimentalBackends,
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		logger.Error(err, "Error setting up controller")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		logger.Error(err, "Error setting up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		logger.Error(err, "Error setting up readiness check")
		os.Exit(1)
	}

	logger.Info("Starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		logger.Error(err, "Error starting manager")
		os.Exit(1)
	}
}
