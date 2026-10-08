package controller

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	discoveryfake "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func TestGatewayAPIChannelCheck(t *testing.T) {
	const min = "v1.6.3"

	tests := []struct {
		name          string
		channel       string
		version       string
		requireXB     bool
		wantWarnCount int
		wantErr       bool
	}{
		{
			name:          "no annotations -> single warning",
			channel:       "",
			version:       "",
			wantWarnCount: 1,
		},
		{
			name:      "experimental at built-against version, xb required -> ok",
			channel:   "experimental",
			version:   min,
			requireXB: true,
		},
		{
			name:    "standard channel with xb required -> fatal",
			channel: "standard",
			version: min,

			requireXB: true,
			wantErr:   true,
		},
		{
			name:    "standard channel without xb -> ok",
			channel: "standard",
			version: min,
		},
		{
			name:      "older bundle with xb required -> fatal",
			channel:   "experimental",
			version:   "v1.5.1",
			requireXB: true,
			wantErr:   true,
		},
		{
			name:          "older bundle without xb -> warning",
			channel:       "experimental",
			version:       "v1.5.1",
			wantWarnCount: 1,
		},
		{
			name:      "earlier patch of the same minor -> ok",
			channel:   "experimental",
			version:   "v1.6.0",
			requireXB: true,
		},
		{
			name:      "release candidate of the same minor -> fatal",
			channel:   "experimental",
			version:   "v1.6.0-rc.1",
			requireXB: true,
			wantErr:   true,
		},
		{
			name:      "newer minor -> fatal for experimental API",
			channel:   "experimental",
			version:   "v1.7.0",
			requireXB: true,
			wantErr:   true,
		},
		{
			name:      "unparseable version with xb -> fatal",
			channel:   "experimental",
			version:   "garbage",
			requireXB: true,
			wantErr:   true,
		},
		{
			name:          "unparseable stable version -> warning",
			channel:       "experimental",
			version:       "garbage",
			wantWarnCount: 1,
		},
		{
			name:          "unannotated custom xb -> warning",
			requireXB:     true,
			wantWarnCount: 1,
		},
		{
			name:          "missing channel still checks version",
			version:       "v1.5.1",
			requireXB:     true,
			wantWarnCount: 1,
			wantErr:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			warnings, err := gatewayAPIChannelCheck(tc.channel, tc.version, min, tc.requireXB)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error: got %v, want error=%v", err, tc.wantErr)
			}
			if len(warnings) != tc.wantWarnCount {
				t.Errorf("warnings: got %d %v, want %d", len(warnings), warnings, tc.wantWarnCount)
			}
		})
	}
}

func TestGatewayAPIBundleMeta(t *testing.T) {
	gvrToListKind := map[schema.GroupVersionResource]string{
		crdGVR: "CustomResourceDefinitionList",
	}

	t.Run("reads annotations", func(t *testing.T) {
		crd := &unstructured.Unstructured{}
		crd.SetGroupVersionKind(schema.GroupVersionKind{Group: crdGVR.Group, Version: crdGVR.Version, Kind: "CustomResourceDefinition"})
		crd.SetName(coreCRDName)
		crd.SetAnnotations(map[string]string{
			channelAnnotation:       "experimental",
			bundleVersionAnnotation: "v1.6.0-rc.1",
		})
		dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind, crd)

		channel, version, err := gatewayAPIBundleMeta(context.Background(), dc, coreCRDName)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if channel != "experimental" || version != "v1.6.0-rc.1" {
			t.Errorf("got channel=%q version=%q", channel, version)
		}
	})

	t.Run("missing CRD returns error", func(t *testing.T) {
		dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind)
		if _, _, err := gatewayAPIBundleMeta(context.Background(), dc, coreCRDName); err == nil {
			t.Error("expected an error reading a missing CRD, got nil")
		}
	})
}

func TestPreflightCheckCRDs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		missing  string
		enabled  bool
		xPresent bool
		xVersion string
		denyMeta bool
		wantErr  string
	}{
		{name: "standard only without XBackend"},
		{name: "standard plus standalone XBackend", enabled: true, xPresent: true, xVersion: "v1.6.1"},
		{name: "enabled but missing XBackend", enabled: true, wantErr: "make install-xbackend-crd"},
		{name: "missing TLSRoute v1", missing: "tlsroutes", wantErr: "\"tlsroutes\" is not served"},
		{name: "missing TCPRoute v1", missing: "tcproutes", wantErr: "\"tcproutes\" is not served"},
		{name: "missing ReferenceGrant v1", missing: "referencegrants", wantErr: "\"referencegrants\" is not served"},
		{name: "missing BackendTLSPolicy v1", missing: "backendtlspolicies", wantErr: "\"backendtlspolicies\" is not served"},
		{name: "wrong XBackend release despite current core", enabled: true, xPresent: true, xVersion: "v1.5.1", wantErr: "XBackend bundle v1.5.1"},
		{name: "unreadable stable metadata is a warning", denyMeta: true},
		{name: "unreadable experimental metadata is fatal", enabled: true, xPresent: true, xVersion: "v1.6.3", denyMeta: true, wantErr: "grant get access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resources := []metav1.APIResource{}
			for _, name := range []string{"gateways", "gatewayclasses", "httproutes", "grpcroutes", "tlsroutes", "tcproutes", "referencegrants", "backendtlspolicies"} {
				if name != tc.missing {
					resources = append(resources, metav1.APIResource{Name: name})
				}
			}
			xResources := []metav1.APIResource{}
			if tc.xPresent {
				xResources = append(xResources, metav1.APIResource{Name: "xbackends"})
			}
			dc := &discoveryfake.FakeDiscovery{Fake: &clienttesting.Fake{Resources: []*metav1.APIResourceList{
				{GroupVersion: "gateway.networking.k8s.io/v1", APIResources: resources},
				{GroupVersion: xBackendGroup + "/v1alpha1", APIResources: xResources},
			}}}
			objects := []runtime.Object{bundleCRD(coreCRDName, "standard", "v1.6.3")}
			if tc.xPresent {
				objects = append(objects, bundleCRD(xBackendCRDName, "experimental", tc.xVersion))
			}
			dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...)
			if tc.denyMeta {
				dyn.PrependReactor("get", "customresourcedefinitions", func(action clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(crdGVR.GroupResource(), action.(clienttesting.GetAction).GetName(), nil)
				})
			}
			err := preflightCheckCRDs(context.Background(), dc, dyn, tc.enabled, "v1.6.3", logr.Discard())
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got error %v, want %q", err, tc.wantErr)
			}
			if !tc.enabled {
				for _, action := range dyn.Actions() {
					if action.(clienttesting.GetAction).GetName() == xBackendCRDName {
						t.Fatal("disabled feature accessed XBackend CRD")
					}
				}
			}
		})
	}
}

func bundleCRD(name, channel, version string) *unstructured.Unstructured {
	crd := &unstructured.Unstructured{}
	crd.SetGroupVersionKind(schema.GroupVersionKind{Group: crdGVR.Group, Version: crdGVR.Version, Kind: "CustomResourceDefinition"})
	crd.SetName(name)
	crd.SetAnnotations(map[string]string{channelAnnotation: channel, bundleVersionAnnotation: version})
	return crd
}

type preflightTransport func(*http.Request) (*http.Response, error)

func (f preflightTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestPreflightRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		want    time.Duration
	}{
		{name: "unset", want: 30 * time.Second},
		{name: "excessive", timeout: time.Minute, want: 30 * time.Second},
		{name: "shorter", timeout: time.Second, want: time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			cfg := &rest.Config{Host: "http://preflight.test", Timeout: tc.timeout, Transport: preflightTransport(func(req *http.Request) (*http.Response, error) {
				called = true
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > tc.want {
					t.Errorf("request deadline must be within %s: %v, set=%v", tc.want, deadline, ok)
				}
				return nil, errors.New("test discovery failure")
			})}
			err := PreflightCheckCRDs(cfg, false, "v1.6.3", logr.Discard())
			if err == nil || !strings.Contains(err.Error(), "test discovery failure") || !called {
				t.Fatalf("expected transport error, got %v (called=%v)", err, called)
			}
			if cfg.Timeout != tc.timeout {
				t.Fatalf("preflight changed the manager config timeout: %s", cfg.Timeout)
			}
		})
	}
}
