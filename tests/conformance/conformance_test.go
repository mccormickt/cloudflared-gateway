package conformance

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/support/kind"

	"sigs.k8s.io/gateway-api/conformance"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	testenv = env.New()
	kindClusterName := envconf.RandomName("cf-conformance", 16)

	testenv.Setup(
		envfuncs.CreateCluster(kind.NewProvider(), kindClusterName),
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			// Install Gateway API CRDs (experimental, includes TLSRoute)
			cmd := exec.Command("kubectl", "apply", "--server-side", "-f",
				"https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.4.1/experimental-install.yaml")
			if out, err := cmd.CombinedOutput(); err != nil {
				return ctx, err
			} else {
				_ = out
			}

			// Wait for CRDs to be established
			time.Sleep(3 * time.Second)
			return ctx, nil
		},
	)

	testenv.Finish(
		envfuncs.DestroyCluster(kindClusterName),
	)

	os.Exit(testenv.Run(m))
}

// TestGatewayAPIConformance runs the official Gateway API conformance suite.
// This requires the controller to be deployed in the cluster.
// Run with: op run --env-file .env -- go test ./tests/conformance/ -v -timeout 30m -args -gateway-class=cloudflare-tunnel
func TestGatewayAPIConformance(t *testing.T) {
	// The conformance suite expects a running controller in the cluster.
	// If SKIP_DEPLOY is not set, we skip this test since the controller
	// needs real Cloudflare credentials and a deployed image.
	if os.Getenv("CLOUDFLARE_API_TOKEN") == "" {
		t.Skip("Skipping conformance tests: CLOUDFLARE_API_TOKEN not set")
	}

	opts := conformance.DefaultOptions(t)
	opts.GatewayClassName = "cloudflare-tunnel"
	opts.EnableAllSupportedFeatures = true
	opts.CleanupBaseResources = true
	opts.Debug = true

	conformance.RunConformanceWithOptions(t, opts)
}
