package controller

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// requiredGatewayAPICRDs are the core Gateway API CRDs the controller cannot run
// without. TLSRoute/TCPRoute (v1alpha2) are treated as optional — they live in
// the experimental channel and the route watches tolerate their absence.
var requiredGatewayAPICRDs = []schema.GroupVersionResource{
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "grpcroutes"},
}

// PreflightCheckCRDs verifies the required Gateway API CRDs are installed,
// failing fast with an actionable message when they are missing. When
// requireXBackend is set (experimental backends enabled), the XBackend CRD
// (gateway.networking.x-k8s.io/v1alpha1) is additionally required.
func PreflightCheckCRDs(cfg *rest.Config, requireXBackend bool) error {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating discovery client: %w", err)
	}

	required := requiredGatewayAPICRDs
	if requireXBackend {
		required = append(required, schema.GroupVersionResource{
			Group:    xBackendGroup,
			Version:  "v1alpha1",
			Resource: "xbackends",
		})
	}

	for _, gvr := range required {
		ok, err := resourceExists(dc, gvr)
		if err != nil {
			return fmt.Errorf("checking for %s.%s: %w", gvr.Resource, gvr.Group, err)
		}
		if !ok {
			if gvr.Group == xBackendGroup {
				return fmt.Errorf("experimental backends are enabled but CRD %q (%s/%s) is not installed; install the Gateway API experimental channel (`make install-crds`) or set experimental.installGatewayAPICRDs=true in the Helm chart", gvr.Resource, gvr.Group, gvr.Version)
			}
			return fmt.Errorf("required Gateway API CRD %q (%s/%s) is not installed; install it with `make install-crds` or set experimental.installGatewayAPICRDs=true in the Helm chart", gvr.Resource, gvr.Group, gvr.Version)
		}
	}
	return nil
}

// resourceExists reports whether the given resource is served by the API server.
func resourceExists(dc discovery.DiscoveryInterface, gvr schema.GroupVersionResource) (bool, error) {
	list, err := dc.ServerResourcesForGroupVersion(gvr.GroupVersion().String())
	if err != nil {
		// A missing group/version surfaces as NotFound — that's a clean "absent".
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	for _, res := range list.APIResources {
		if res.Name == gvr.Resource {
			return true, nil
		}
	}
	return false, nil
}
