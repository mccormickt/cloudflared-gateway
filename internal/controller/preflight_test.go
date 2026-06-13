package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestGatewayAPIChannelCheck(t *testing.T) {
	const min = "v1.6.0-rc.1"

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
			name:          "standard channel without xb -> warning",
			channel:       "standard",
			version:       min,
			wantWarnCount: 1,
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
			name:      "newer GA bundle beats built-against rc -> ok",
			channel:   "experimental",
			version:   "v1.6.0",
			requireXB: true,
		},
		{
			name:          "unparseable version -> warning, no error",
			channel:       "experimental",
			version:       "garbage",
			requireXB:     true,
			wantWarnCount: 1,
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

		channel, version, err := gatewayAPIBundleMeta(context.Background(), dc)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if channel != "experimental" || version != "v1.6.0-rc.1" {
			t.Errorf("got channel=%q version=%q", channel, version)
		}
	})

	t.Run("missing CRD returns error", func(t *testing.T) {
		dc := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrToListKind)
		if _, _, err := gatewayAPIBundleMeta(context.Background(), dc); err == nil {
			t.Error("expected an error reading a missing CRD, got nil")
		}
	})
}
