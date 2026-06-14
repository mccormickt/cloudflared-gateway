package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"golang.org/x/mod/semver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
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

// Gateway API stamps its release channel and bundle version onto every CRD it
// ships via these annotations. We read them off a core CRD to sanity-check the
// installed bundle against what the controller was built for.
const (
	bundleVersionAnnotation = "gateway.networking.k8s.io/bundle-version"
	channelAnnotation       = "gateway.networking.k8s.io/channel"
	experimentalChannel     = "experimental"

	// coreCRDName is the CRD whose annotations we inspect — gateways is always
	// present once the core channel is installed.
	coreCRDName = "gateways.gateway.networking.k8s.io"
)

// crdGVR is the apiextensions CustomResourceDefinition resource, fetched
// dynamically so we can read a CRD's annotations without depending on the
// apiextensions client-go typed client.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// PreflightCheckCRDs verifies the required Gateway API CRDs are installed,
// failing fast with an actionable message when they are missing. When
// requireXBackend is set (experimental backends enabled), the XBackend CRD
// (gateway.networking.x-k8s.io/v1alpha1) is additionally required.
//
// It then reads the installed bundle's channel/version annotations and compares
// them against minBundleVersion (the Gateway API version this controller was
// built against). Mismatches are warnings by default; with requireXBackend they
// are fatal, because the experimental APIs drift between bundle versions.
func PreflightCheckCRDs(cfg *rest.Config, requireXBackend bool, minBundleVersion string, log logr.Logger) error {
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

	// Channel/version compatibility. A read failure here is non-fatal: presence
	// is already confirmed, and an unreadable annotation shouldn't block startup.
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}
	channel, version, err := gatewayAPIBundleMeta(context.Background(), dyn)
	if err != nil {
		log.Info("Skipping Gateway API channel/version compatibility check: could not read bundle metadata", "error", err.Error())
		return nil
	}

	warnings, fatal := gatewayAPIChannelCheck(channel, version, minBundleVersion, requireXBackend)
	for _, w := range warnings {
		log.Info("Gateway API compatibility warning", "detail", w)
	}
	return fatal
}

// gatewayAPIBundleMeta reads the channel and bundle-version annotations off the
// core gateways CRD. Empty strings mean the annotation was absent.
func gatewayAPIBundleMeta(ctx context.Context, dyn dynamic.Interface) (channel, version string, err error) {
	obj, err := dyn.Resource(crdGVR).Get(ctx, coreCRDName, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	ann := obj.GetAnnotations()
	return ann[channelAnnotation], ann[bundleVersionAnnotation], nil
}

// gatewayAPIChannelCheck evaluates the installed bundle's channel/version against
// minBundleVersion. It returns non-fatal warnings to log plus a fatal error when
// requireXBackend cannot be satisfied (wrong channel, or a bundle older than the
// one this controller was built against). Empty channel/version means the
// annotations were absent, which yields a single warning and no error.
func gatewayAPIChannelCheck(channel, bundleVersion, minBundleVersion string, requireXBackend bool) (warnings []string, fatal error) {
	if channel == "" && bundleVersion == "" {
		return []string{"installed Gateway API CRDs carry no channel/bundle-version annotations; cannot verify compatibility"}, nil
	}

	if channel != "" && channel != experimentalChannel {
		if requireXBackend {
			return warnings, fmt.Errorf("experimental backends are enabled but the installed Gateway API CRDs are the %q channel; XBackend and the experimental route types live in the experimental channel — reinstall with `make install-crds` (experimental channel) or disable experimental backends", channel)
		}
		warnings = append(warnings, fmt.Sprintf("installed Gateway API CRDs are the %q channel; experimental route types (TLSRoute/TCPRoute) may be absent", channel))
	}

	if bundleVersion != "" && minBundleVersion != "" {
		switch {
		case !semver.IsValid(bundleVersion) || !semver.IsValid(minBundleVersion):
			warnings = append(warnings, fmt.Sprintf("could not compare Gateway API bundle version %q against the built-against version %q", bundleVersion, minBundleVersion))
		case semver.Compare(bundleVersion, minBundleVersion) < 0:
			msg := fmt.Sprintf("installed Gateway API bundle %s is older than the version this controller was built against (%s); API shapes may differ", bundleVersion, minBundleVersion)
			if requireXBackend {
				return warnings, fmt.Errorf("%s — reinstall the Gateway API CRDs at %s or newer, or disable experimental backends", msg, minBundleVersion)
			}
			warnings = append(warnings, msg)
		}
	}

	return warnings, nil
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
