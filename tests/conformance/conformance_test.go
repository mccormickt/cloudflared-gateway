package conformance

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
				fmt.Sprintf("https://github.com/kubernetes-sigs/gateway-api/releases/download/%s/experimental-install.yaml", gatewayAPIVersion()))
			if out, err := cmd.CombinedOutput(); err != nil {
				return ctx, fmt.Errorf("installing Gateway API CRDs: %s: %w", string(out), err)
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

func gatewayAPIVersion() string {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "sigs.k8s.io/gateway-api").Output()
	if err != nil {
		panic("failed to find gateway-api module version: " + err.Error())
	}
	return strings.TrimSpace(string(out))
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
