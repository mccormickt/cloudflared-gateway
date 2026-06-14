package controller

import (
	"context"
	"fmt"

	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	apisxv1alpha1 "sigs.k8s.io/gateway-api/apisx/v1alpha1"
)

// xBackendGroup/xBackendKind identify the experimental Gateway API XBackend
// resource (gateway.networking.x-k8s.io/v1alpha1) that route backendRefs may
// target for external (non-cluster) destinations.
const (
	xBackendGroup = apisxv1alpha1.GroupName
	xBackendKind  = "XBackend"
)

// Reasons recorded against a route's ResolvedRefs condition when one of its
// XBackend refs cannot be served. The empty string means resolved/OK.
const (
	reasonResolvedOK          = ""
	reasonInvalidKind         = "InvalidKind"         // XBackend ref but experimental support is disabled
	reasonBackendNotFound     = "BackendNotFound"     // XBackend object does not exist
	reasonRefNotPermitted     = "RefNotPermitted"     // cross-namespace ref without a ReferenceGrant
	reasonUnsupportedProtocol = "UnsupportedProtocol" // a protocol/TLS mode cloudflared tunnels can't serve
	reasonUnsupportedCACerts  = "UnsupportedCACerts"  // TLS validation pins custom caCertificateRefs we can't provision
)

// xbKey identifies an XBackend object.
type xbKey struct {
	namespace string
	name      string
}

// permitKey identifies a single cross-namespace authorization: a route of a
// given kind in routeNS referencing an XBackend in a different namespace.
type permitKey struct {
	routeNS   string
	routeKind string
	xbNS      string
	name      string
}

// xbBackends is the per-reconcile resolution context for XBackend refs: the
// fetched objects, cross-namespace grant outcomes, and the set of XBackends
// referenced by attached routes (for status). It is nil when experimental
// backend support is disabled.
type xbBackends struct {
	fetched    map[xbKey]*apisxv1alpha1.XBackend
	permitted  map[permitKey]bool
	referenced map[xbKey]bool
}

// xbRefTarget is a single XBackend backendRef discovered on an attached route.
// The ref's port is intentionally omitted: an XBackend's own spec.port is
// authoritative for the connection.
type xbRefTarget struct {
	routeNS   string
	routeKind string
	xbNS      string
	name      string
}

// isXBackendRef reports whether a backendRef targets an XBackend.
func isXBackendRef(r gwapiv1.BackendObjectReference) bool {
	group := ""
	if r.Group != nil {
		group = string(*r.Group)
	}
	kind := "Service"
	if r.Kind != nil {
		kind = string(*r.Kind)
	}
	return group == xBackendGroup && kind == xBackendKind
}

// xbTargetsFromObjRefs filters a route's backendRefs to its XBackend targets,
// defaulting the backend namespace to the route's namespace.
func xbTargetsFromObjRefs(routeNS, routeKind string, refs []gwapiv1.BackendObjectReference) []xbRefTarget {
	var out []xbRefTarget
	for _, r := range refs {
		if !isXBackendRef(r) {
			continue
		}
		ns := routeNS
		if r.Namespace != nil {
			ns = string(*r.Namespace)
		}
		out = append(out, xbRefTarget{routeNS: routeNS, routeKind: routeKind, xbNS: ns, name: string(r.Name)})
	}
	return out
}

// Per-route-type backendRef extractors.
func httpRouteObjRefs(route *gwapiv1.HTTPRoute) []gwapiv1.BackendObjectReference {
	var out []gwapiv1.BackendObjectReference
	for ri := range route.Spec.Rules {
		for bi := range route.Spec.Rules[ri].BackendRefs {
			out = append(out, route.Spec.Rules[ri].BackendRefs[bi].BackendObjectReference)
		}
	}
	return out
}

func grpcRouteObjRefs(route *gwapiv1.GRPCRoute) []gwapiv1.BackendObjectReference {
	var out []gwapiv1.BackendObjectReference
	for ri := range route.Spec.Rules {
		for bi := range route.Spec.Rules[ri].BackendRefs {
			out = append(out, route.Spec.Rules[ri].BackendRefs[bi].BackendObjectReference)
		}
	}
	return out
}

func tlsRouteObjRefs(route *gwapiv1alpha2.TLSRoute) []gwapiv1.BackendObjectReference {
	var out []gwapiv1.BackendObjectReference
	for ri := range route.Spec.Rules {
		for bi := range route.Spec.Rules[ri].BackendRefs {
			out = append(out, route.Spec.Rules[ri].BackendRefs[bi].BackendObjectReference)
		}
	}
	return out
}

func tcpRouteObjRefs(route *gwapiv1alpha2.TCPRoute) []gwapiv1.BackendObjectReference {
	var out []gwapiv1.BackendObjectReference
	for ri := range route.Spec.Rules {
		for bi := range route.Spec.Rules[ri].BackendRefs {
			out = append(out, route.Spec.Rules[ri].BackendRefs[bi].BackendObjectReference)
		}
	}
	return out
}

// collectReferencedXBackends discovers every XBackend referenced by an attached
// route, authorizes cross-namespace references via ReferenceGrant, and fetches
// the permitted objects. The returned context is consumed by the resolver and
// by route/XBackend status patching.
func (r *GatewayReconciler) collectReferencedXBackends(
	ctx context.Context,
	http []gwapiv1.HTTPRoute,
	grpc []gwapiv1.GRPCRoute,
	tls []gwapiv1alpha2.TLSRoute,
	tcp []gwapiv1alpha2.TCPRoute,
) (*xbBackends, error) {
	col := &xbBackends{
		fetched:    map[xbKey]*apisxv1alpha1.XBackend{},
		permitted:  map[permitKey]bool{},
		referenced: map[xbKey]bool{},
	}

	targets := make([]xbRefTarget, 0, len(http)+len(grpc)+len(tls)+len(tcp))
	for i := range http {
		targets = append(targets, xbTargetsFromObjRefs(http[i].Namespace, "HTTPRoute", httpRouteObjRefs(&http[i]))...)
	}
	for i := range grpc {
		targets = append(targets, xbTargetsFromObjRefs(grpc[i].Namespace, "GRPCRoute", grpcRouteObjRefs(&grpc[i]))...)
	}
	for i := range tls {
		targets = append(targets, xbTargetsFromObjRefs(tls[i].Namespace, "TLSRoute", tlsRouteObjRefs(&tls[i]))...)
	}
	for i := range tcp {
		targets = append(targets, xbTargetsFromObjRefs(tcp[i].Namespace, "TCPRoute", tcpRouteObjRefs(&tcp[i]))...)
	}

	for _, t := range targets {
		key := xbKey{t.xbNS, t.name}
		col.referenced[key] = true

		if t.routeNS != t.xbNS {
			pk := permitKey(t)
			if _, done := col.permitted[pk]; !done {
				ok, err := CheckReferenceGrantTo(ctx, r.Client, t.routeNS, t.routeKind, t.xbNS, xBackendGroup, xBackendKind, t.name)
				if err != nil {
					return nil, err
				}
				col.permitted[pk] = ok
			}
			if !col.permitted[pk] {
				continue
			}
		}

		if _, done := col.fetched[key]; done {
			continue
		}
		var xb apisxv1alpha1.XBackend
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: t.xbNS, Name: t.name}, &xb); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, err
		}
		col.fetched[key] = &xb
	}

	return col, nil
}

// resolveXBackendRef resolves a single XBackend backendRef to a tunnel service +
// origin, or a non-empty reason explaining why it can't be served. col is nil
// when experimental support is disabled.
func (r *GatewayReconciler) resolveXBackendRef(routeNS, routeKind, xbNS, name string, col *xbBackends) (cfclient.ResolvedBackend, string) {
	if !r.ExperimentalBackends || col == nil {
		return cfclient.ResolvedBackend{}, reasonInvalidKind
	}
	if routeNS != xbNS && !col.permitted[permitKey{routeNS, routeKind, xbNS, name}] {
		return cfclient.ResolvedBackend{}, reasonRefNotPermitted
	}
	xb := col.fetched[xbKey{xbNS, name}]
	if xb == nil {
		return cfclient.ResolvedBackend{}, reasonBackendNotFound
	}
	return translateXBackend(xb)
}

// backendResolver returns the cloudflare BackendResolver used while building
// ingress rules. It claims every XBackend ref (so a disabled/unresolvable ref
// becomes http_status:503 rather than being mistaken for an in-cluster Service)
// and declines all other refs to the native Service path.
func (r *GatewayReconciler) backendResolver(col *xbBackends) cfclient.BackendResolver {
	return func(ref cfclient.BackendRef) (cfclient.ResolvedBackend, bool) {
		if ref.Group != xBackendGroup || ref.Kind != xBackendKind {
			return cfclient.ResolvedBackend{}, false
		}
		rb, _ := r.resolveXBackendRef(ref.RouteNamespace, ref.RouteKind, ref.Namespace, ref.Name, col)
		return rb, true
	}
}

// translateXBackend maps an XBackend spec to a Cloudflare tunnel service URL and
// the originRequest deltas its protocol/TLS settings imply. A non-empty reason
// means the backend can't be served (unsupported protocol or TLS mode).
func translateXBackend(xb *apisxv1alpha1.XBackend) (cfclient.ResolvedBackend, string) {
	if xb.Spec.Type != apisxv1alpha1.BackendTypeExternalHostname || xb.Spec.ExternalHostname == nil {
		return cfclient.ResolvedBackend{}, reasonUnsupportedProtocol
	}
	host := string(xb.Spec.ExternalHostname.Hostname)
	port := int(xb.Spec.Port.Port)

	proto := apisxv1alpha1.BackendProtocolHTTP
	if xb.Spec.Protocol != nil {
		proto = *xb.Spec.Protocol
	}
	// Cloudflare tunnels can't proxy MCP as a first-class protocol.
	if proto == apisxv1alpha1.BackendProtocolMCP {
		return cfclient.ResolvedBackend{}, reasonUnsupportedProtocol
	}

	usesTLS := xb.Spec.TLS != nil && xb.Spec.TLS.Mode != apisxv1alpha1.BackendTLSModeNone

	// cloudflared's tcp:// proxy is an opaque byte stream: it cannot perform
	// origin TLS verification, so a TCP backend that asks for TLS would be
	// silently downgraded to a raw connection. Fail closed instead.
	if proto == apisxv1alpha1.BackendProtocolTCP && usesTLS {
		return cfclient.ResolvedBackend{}, reasonUnsupportedProtocol
	}

	origin := &cfclient.OriginRequest{}
	switch proto {
	case apisxv1alpha1.BackendProtocolHTTP2, apisxv1alpha1.BackendProtocolH2C, apisxv1alpha1.BackendProtocolGRPC:
		t := true
		origin.HTTP2Origin = &t
	}

	scheme := "http"
	switch {
	case proto == apisxv1alpha1.BackendProtocolTCP:
		scheme = "tcp"
	case usesTLS:
		scheme = "https"
	}

	if xb.Spec.TLS != nil {
		switch xb.Spec.TLS.Mode {
		case apisxv1alpha1.BackendTLSModeClientAndServer:
			// mTLS to the origin requires presenting a client certificate, which
			// remote-managed cloudflared tunnels cannot do.
			return cfclient.ResolvedBackend{}, reasonUnsupportedProtocol
		case apisxv1alpha1.BackendTLSModeServerOnly:
			// A custom CA pin (caCertificateRefs) would have to be provisioned into
			// the cloudflared Deployment and pointed at via originRequest.caPool,
			// which isn't wired up — verifying against the system CAs instead would
			// silently break a connection that pinned a private CA. Surface it.
			if len(xb.Spec.TLS.Validation.CACertificateRefs) > 0 {
				return cfclient.ResolvedBackend{}, reasonUnsupportedCACerts
			}
			// Verify the origin certificate (NoTLSVerify left unset) and use the
			// validation hostname as the SNI server name when provided.
			if h := string(xb.Spec.TLS.Validation.Hostname); h != "" {
				origin.OriginServerName = &h
			}
		case apisxv1alpha1.BackendTLSModeNone:
			// Plain connection; scheme stays http (or tcp).
		}
	}

	if originRequestEmpty(origin) {
		origin = nil
	}
	return cfclient.ResolvedBackend{
		Service:       fmt.Sprintf("%s://%s:%d", scheme, host, port),
		OriginRequest: origin,
	}, reasonResolvedOK
}

// originRequestEmpty reports whether translateXBackend set any origin field.
func originRequestEmpty(o *cfclient.OriginRequest) bool {
	return o.HTTP2Origin == nil && o.OriginServerName == nil && o.NoTLSVerify == nil
}

// reasonSeverity orders ResolvedRefs reasons so a route with several failing
// refs reports the most actionable one.
func reasonSeverity(reason string) int {
	switch reason {
	case reasonInvalidKind:
		return 5
	case reasonRefNotPermitted:
		return 4
	case reasonBackendNotFound:
		return 3
	case reasonUnsupportedProtocol:
		return 2
	case reasonUnsupportedCACerts:
		return 1
	default:
		return 0
	}
}

// routeResolvedRefs computes a route's ResolvedRefs result from its XBackend
// refs. Routes with no XBackend refs resolve OK.
func (r *GatewayReconciler) routeResolvedRefs(routeNS, routeKind string, objRefs []gwapiv1.BackendObjectReference, col *xbBackends) ResolvedRefsResult {
	worst := reasonResolvedOK
	for _, t := range xbTargetsFromObjRefs(routeNS, routeKind, objRefs) {
		_, reason := r.resolveXBackendRef(t.routeNS, t.routeKind, t.xbNS, t.name, col)
		if reasonSeverity(reason) > reasonSeverity(worst) {
			worst = reason
		}
	}
	return resolvedRefsResultFor(worst)
}
