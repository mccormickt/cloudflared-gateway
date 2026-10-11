package controller

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/go-logr/logr"
	"golang.org/x/mod/semver"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// requiredGatewayAPICRDs includes every stable Gateway API resource the
// controller watches. All are available in the standard v1.6 bundle.
var requiredGatewayAPICRDs = []schema.GroupVersionResource{
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "grpcroutes"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "tlsroutes"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "tcproutes"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "backendtlspolicies"},
	{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "referencegrants"},
}

// Gateway API stamps its release channel and bundle version onto each CRD.
const (
	bundleVersionAnnotation = "gateway.networking.k8s.io/bundle-version"
	channelAnnotation       = "gateway.networking.k8s.io/channel"
	experimentalChannel     = "experimental"

	coreCRDName     = "gateways.gateway.networking.k8s.io"
	xBackendCRDName = "xbackends." + xBackendGroup
)

// crdGVR is the apiextensions CustomResourceDefinition resource, fetched
// dynamically so we can read a CRD's annotations without depending on the
// apiextensions client-go typed client.
var crdGVR = schema.GroupVersionResource{
	Group:    "apiextensions.k8s.io",
	Version:  "v1",
	Resource: "customresourcedefinitions",
}

// PreflightCheckCRDs checks served API versions before starting any watches.
// XBackend is required only when enabled. Its own CRD metadata must match the
// built-against release line; the stable CRDs may remain on the standard channel.
func PreflightCheckCRDs(cfg *rest.Config, requireXBackend bool, builtVersion string, log logr.Logger) error {
	cfg = rest.CopyConfig(cfg)
	if cfg.Timeout <= 0 || cfg.Timeout > 30*time.Second {
		cfg.Timeout = 30 * time.Second
	}
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating discovery client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("creating dynamic client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return preflightCheckCRDs(ctx, dc, dyn, requireXBackend, builtVersion, log)
}

func preflightCheckCRDs(ctx context.Context, dc discovery.DiscoveryInterface, dyn dynamic.Interface, requireXBackend bool, builtVersion string, log logr.Logger) error {
	// Copy, so appending never writes into the package-level slice's backing array.
	required := slices.Clone(requiredGatewayAPICRDs)
	if requireXBackend {
		required = append(required, schema.GroupVersionResource{
			Group:    xBackendGroup,
			Version:  "v1alpha1",
			Resource: "xbackends",
		})
	}

	for _, gvr := range required {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("checking Gateway API resources: %w", err)
		}
		ok, err := resourceExists(dc, gvr)
		if err != nil {
			return fmt.Errorf("checking for %s.%s: %w", gvr.Resource, gvr.Group, err)
		}
		if !ok {
			if gvr.Group == xBackendGroup {
				return fmt.Errorf("experimental backends are enabled but %s/%s resource %q is not served; install the XBackend CRD from Gateway API %s (`make install-xbackend-crd`) or disable experimental.backends.enabled / --enable-experimental-backends", gvr.Group, gvr.Version, gvr.Resource, builtVersion)
			}
			return fmt.Errorf("required Gateway API resource %s/%s %q is not served; install the Gateway API %s standard bundle (`make install-crds`) or set experimental.installGatewayAPICRDs=true in the Helm chart", gvr.Group, gvr.Version, gvr.Resource, builtVersion)
		}
	}

	crdNames := []string{coreCRDName}
	if requireXBackend {
		crdNames = append(crdNames, xBackendCRDName)
	}
	for _, name := range crdNames {
		experimental := name == xBackendCRDName
		channel, version, err := gatewayAPIBundleMeta(ctx, dyn, name)
		if err != nil {
			if experimental {
				return fmt.Errorf("cannot verify XBackend CRD compatibility: %w; grant get access to customresourcedefinitions.apiextensions.k8s.io", err)
			}
			log.Info("Cannot read Gateway API bundle metadata", "crd", name, "error", err.Error())
			continue
		}
		warnings, fatal := gatewayAPIChannelCheck(channel, version, builtVersion, experimental)
		for _, w := range warnings {
			log.Info("Gateway API compatibility warning", "crd", name, "detail", w)
		}
		if fatal != nil {
			return fmt.Errorf("CRD %s: %w", name, fatal)
		}
	}
	return nil
}

// gatewayAPIBundleMeta reads one CRD's bundle annotations. An absent annotation
// is returned as an empty string.
func gatewayAPIBundleMeta(ctx context.Context, dyn dynamic.Interface, name string) (channel, version string, err error) {
	obj, err := dyn.Resource(crdGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", "", err
	}
	ann := obj.GetAnnotations()
	return ann[channelAnnotation], ann[bundleVersionAnnotation], nil
}

// gatewayAPIChannelCheck permits stable resources from either channel. For
// XBackend, annotated CRDs must use the experimental channel and the same GA
// major.minor release line. Missing annotations produce a warning for custom
// installations whose served API version was already checked.
func gatewayAPIChannelCheck(channel, bundleVersion, builtVersion string, requireXBackend bool) (warnings []string, fatal error) {
	if channel == "" || bundleVersion == "" {
		warnings = append(warnings, "CRD channel or bundle-version annotation is missing; cannot fully verify compatibility")
	}

	if channel != "" && channel != experimentalChannel {
		if requireXBackend {
			return warnings, fmt.Errorf("XBackend requires an experimental-channel CRD, got %q; install with `make install-xbackend-crd` or disable experimental backends", channel)
		}
		if channel != "standard" {
			warnings = append(warnings, fmt.Sprintf("unknown Gateway API CRD channel %q", channel))
		}
	}

	if bundleVersion != "" && builtVersion != "" {
		comparison := semver.Compare(semver.MajorMinor(bundleVersion), semver.MajorMinor(builtVersion))
		switch {
		case !semver.IsValid(bundleVersion) || !semver.IsValid(builtVersion):
			msg := fmt.Sprintf("cannot compare CRD bundle version %q with built-against version %q", bundleVersion, builtVersion)
			if requireXBackend {
				return warnings, fmt.Errorf("%s; install with `make install-xbackend-crd` or disable experimental backends", msg)
			}
			warnings = append(warnings, msg)
		case requireXBackend && (comparison != 0 || semver.Prerelease(bundleVersion) != ""):
			return warnings, fmt.Errorf("XBackend bundle %s is not compatible with the built-against release line %s; install the %s XBackend CRD (`make install-xbackend-crd`) or disable experimental backends", bundleVersion, semver.MajorMinor(builtVersion), builtVersion)
		case comparison < 0:
			warnings = append(warnings, fmt.Sprintf("CRD bundle %s is older than built-against version %s", bundleVersion, builtVersion))
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
