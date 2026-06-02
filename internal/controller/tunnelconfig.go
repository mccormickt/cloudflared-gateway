package controller

import (
	"context"
	"fmt"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const tunnelConfigKind = "CloudflareTunnelConfig"

// resolveTunnelConfig resolves the effective CloudflareTunnelConfig for a Gateway.
// A GatewayClass.spec.parametersRef provides a cluster-wide default; a
// Gateway.spec.infrastructure.parametersRef overrides it per-Gateway. An invalid
// or missing reference is treated as no override (built-in defaults apply) and
// does not fail reconciliation. Returns the merged spec (nil when neither ref
// applies) plus the validity of the GatewayClass-level reference: classParamsOK
// is false (with a message) only when a GatewayClass parametersRef is present
// but cannot be resolved — the caller surfaces this on the GatewayClass status.
func (r *GatewayReconciler) resolveTunnelConfig(ctx context.Context, gw *gwapiv1.Gateway, gc *gwapiv1.GatewayClass) (spec *cfv1alpha1.CloudflareTunnelConfigSpec, classParamsOK bool, classParamsMsg string) {
	logger := log.FromContext(ctx)
	classParamsOK = true
	var merged *cfv1alpha1.CloudflareTunnelConfigSpec

	// GatewayClass-level default (cluster-wide). GatewayClass is cluster-scoped,
	// so the ref must carry a namespace.
	if ref := gc.Spec.ParametersRef; ref != nil && string(ref.Group) == cfv1alpha1.GroupVersion.Group && string(ref.Kind) == tunnelConfigKind {
		ns := ""
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}
		if ns == "" {
			classParamsOK = false
			classParamsMsg = fmt.Sprintf("parametersRef to CloudflareTunnelConfig %q requires a namespace", ref.Name)
			logger.Error(fmt.Errorf("missing namespace"), "GatewayClass parametersRef requires a namespace", "gatewayClass", gc.Name, "name", ref.Name)
		} else if cfg, err := r.fetchTunnelConfig(ctx, ns, ref.Name); err != nil {
			classParamsOK = false
			classParamsMsg = err.Error()
			logger.Error(err, "Invalid GatewayClass parametersRef, using defaults", "gatewayClass", gc.Name)
		} else {
			merged = cfg
		}
	}

	// Gateway-level override (same namespace as the Gateway).
	if gw.Spec.Infrastructure != nil {
		if ref := gw.Spec.Infrastructure.ParametersRef; ref != nil && string(ref.Group) == cfv1alpha1.GroupVersion.Group && string(ref.Kind) == tunnelConfigKind {
			if cfg, err := r.fetchTunnelConfig(ctx, gw.Namespace, ref.Name); err != nil {
				logger.Error(err, "Invalid Gateway infrastructure parametersRef, using GatewayClass/default config", "gateway", gw.Name)
			} else {
				merged = mergeTunnelConfigSpec(merged, cfg)
			}
		}
	}

	return merged, classParamsOK, classParamsMsg
}

func (r *GatewayReconciler) fetchTunnelConfig(ctx context.Context, namespace, name string) (*cfv1alpha1.CloudflareTunnelConfigSpec, error) {
	var cfg cfv1alpha1.CloudflareTunnelConfig
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &cfg); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("CloudflareTunnelConfig %s/%s not found", namespace, name)
		}
		return nil, fmt.Errorf("getting CloudflareTunnelConfig %s/%s: %w", namespace, name, err)
	}
	return &cfg.Spec, nil
}

// mergeTunnelConfigSpec overlays override onto base field-by-field; override wins.
func mergeTunnelConfigSpec(base, override *cfv1alpha1.CloudflareTunnelConfigSpec) *cfv1alpha1.CloudflareTunnelConfigSpec {
	if base == nil {
		return override
	}
	if override == nil {
		return base
	}
	out := *base
	if override.Replicas != nil {
		out.Replicas = override.Replicas
	}
	if override.Image != nil {
		out.Image = override.Image
	}
	if override.Resources != nil {
		out.Resources = override.Resources
	}
	if override.LogLevel != nil {
		out.LogLevel = override.LogLevel
	}
	if override.MetricsPort != nil {
		out.MetricsPort = override.MetricsPort
	}
	if override.PodLabels != nil {
		out.PodLabels = override.PodLabels
	}
	if override.PodAnnotations != nil {
		out.PodAnnotations = override.PodAnnotations
	}
	if override.NodeSelector != nil {
		out.NodeSelector = override.NodeSelector
	}
	if override.Tolerations != nil {
		out.Tolerations = override.Tolerations
	}
	if override.Affinity != nil {
		out.Affinity = override.Affinity
	}
	return &out
}
