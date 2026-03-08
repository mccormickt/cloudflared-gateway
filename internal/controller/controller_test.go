package controller

import (
	"context"
	"fmt"
	"io"
	"os"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/envfuncs"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"sigs.k8s.io/e2e-framework/support/kind"
	"sigs.k8s.io/e2e-framework/support/utils"
)

var testenv env.Environment

const testNamespace = "cloudflared"

var namespace = envconf.RandomName(testNamespace, 16)

func TestMain(m *testing.M) {
	testenv = env.New()
	testenv = env.New()
	kindClusterName := envconf.RandomName("cloudflared-test", 16)

	// Create a kind cluster to run the tests in
	testenv.Setup(
		envfuncs.CreateCluster(kind.NewProvider(), kindClusterName),
		envfuncs.CreateNamespace(namespace),
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			// Install the Gateway API CRDs
			if p := utils.RunCommand("kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.0.0/standard-install.yaml"); p.Err() != nil {
				out, _ := io.ReadAll(p.Out())
				fmt.Println(string(out))
				os.Exit(1)
			}

			// Create a gateway class
			if p := utils.RunCommand("kubectl apply -f ../../examples/gatewayclass.yaml"); p.Err() != nil {
				out, _ := io.ReadAll(p.Out())
				fmt.Println(string(out))
				os.Exit(1)
			}

			// Create a gateway
			if p := utils.RunCommand("kubectl apply -f ../../examples/gateway.yaml"); p.Err() != nil {
				out, _ := io.ReadAll(p.Out())
				fmt.Println(string(out))
				os.Exit(1)
			}
			return ctx, nil
		},
	)

	// Tear down the kind cluster after the tests have run
	testenv.Finish(
		func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
			utils.RunCommand(fmt.Sprintf("kubectl delete -f %s", "examples/gateway.yaml"))
			utils.RunCommand(fmt.Sprintf("kubectl delete -f %s", "examples/gatewayclass.yaml"))
			return ctx, nil
		},
		envfuncs.DeleteNamespace(namespace),
		envfuncs.DestroyCluster(kindClusterName),
	)

	// launch package tests
	os.Exit(testenv.Run(m))
}

func TestDeployCloudflared(t *testing.T) {
	f := features.New("check cloudflared deployment")
	f.Setup(func(ctx context.Context, t *testing.T, cfg *envconf.Config) context.Context {
		c, err := client.New(cfg.Client().RESTConfig(), client.Options{})
		if err != nil {
			t.Fatalf("Unable to create client: %v", err)
		}
		client := &tunnelReconciler{client: c}

		if err := client.deployCloudflared(ctx, namespace); err != nil {
			t.Fatalf("Unable to deploy cloudflared: %v", err)
		}
		t.Logf("Deployed cloudflared to %s", namespace)
		return ctx
	})

	f.Assess("Cloudflared deployed", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client := c.Client()

		// Check that the cloudflared deployment exists
		var deployment appsv1.Deployment
		if err := client.Resources().Get(ctx, "cloudflared-gateway", namespace, &deployment); err != nil {
			t.Fatalf("Unable to get deployment: %v", err)
		}
		return ctx
	})

	testenv.Test(t, f.Feature())
}
