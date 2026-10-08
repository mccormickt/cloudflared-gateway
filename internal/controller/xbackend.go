package controller

import (
	"context"
	"fmt"

	cfclient "github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
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
// backendRefs cannot be served. The empty string means resolved/OK.
const (
	reasonResolvedOK               = ""
	reasonXBackendDisabled         = "XBackendDisabled"         // XBackend ref but experimental support is disabled
	reasonUnsupportedKind          = "UnsupportedKind"          // backendRef names a kind we can't route to
	reasonBackendNotFound          = "BackendNotFound"          // referenced object does not exist
	reasonRefNotPermitted          = "RefNotPermitted"          // cross-namespace ref without a ReferenceGrant
	reasonIncompatibleRouteKind    = "IncompatibleRouteKind"    // backend can't serve this route kind's transport
	reasonUnsupportedProtocol      = "UnsupportedProtocol"      // a protocol/TLS mode cloudflared tunnels can't serve
	reasonUnsupportedCACerts       = "UnsupportedCACerts"       // TLS validation pins custom caCertificateRefs we can't provision
	reasonUnsupportedTLSValidation = "UnsupportedTLSValidation" // custom certificate SAN matching cannot be enforced
)

// xbKey identifies an XBackend object.
type xbKey struct {
	namespace string
	name      string
}

// permitKey identifies a single cross-namespace authorization: a route of a
// given kind in routeNS referencing a backend in a different namespace.
type permitKey struct {
	routeNS   string
	routeKind string
	toNS      string
	name      string
}

// xbBackends is the per-reconcile resolution context for XBackend refs: the
// XBackends referenced by attached routes that were permitted and successfully
// fetched, plus the cross-namespace grant outcomes. It is nil when experimental
// backend support is disabled.
type xbBackends struct {
	fetched   map[xbKey]*apisxv1alpha1.XBackend
	permitted map[permitKey]bool
}

// backendTarget is a single backendRef discovered on an attached route, with
// Gateway API defaulting applied: an omitted group means core, an omitted kind
// means Service, and an omitted namespace means the route's own. The ref's port
// is intentionally omitted — for an XBackend its own spec.port is authoritative,
// and the native Service path reads the port straight off the ref.
type backendTarget struct {
	routeNS   string
	routeKind string
	group     string
	kind      string
	ns        string
	name      string
}

// isXBackend reports whether the target is an experimental XBackend.
func (t backendTarget) isXBackend() bool {
	return t.group == xBackendGroup && t.kind == xBackendKind
}

// isService reports whether the target is an in-cluster core Service.
func (t backendTarget) isService() bool {
	return (t.group == "" || t.group == "core") && t.kind == serviceKind
}

// permit builds this target's cross-namespace authorization key.
func (t backendTarget) permit() permitKey {
	return permitKey{routeNS: t.routeNS, routeKind: t.routeKind, toNS: t.ns, name: t.name}
}

// backendRefGroupKind applies Gateway API defaulting to a backendRef's group
// and kind: an omitted group means core, an omitted kind means Service.
func backendRefGroupKind(r gwapiv1.BackendObjectReference) (group, kind string) {
	kind = serviceKind
	if r.Group != nil {
		group = string(*r.Group)
	}
	if r.Kind != nil {
		kind = string(*r.Kind)
	}
	return group, kind
}

// isXBackendRef reports whether a backendRef targets an XBackend.
func isXBackendRef(r gwapiv1.BackendObjectReference) bool {
	group, kind := backendRefGroupKind(r)
	return group == xBackendGroup && kind == xBackendKind
}

// targetsFromObjRefs applies Gateway API defaulting to a route's backendRefs.
func targetsFromObjRefs(routeNS, routeKind string, refs []gwapiv1.BackendObjectReference) []backendTarget {
	out := make([]backendTarget, 0, len(refs))
	for _, r := range refs {
		t := backendTarget{routeNS: routeNS, routeKind: routeKind, ns: routeNS, name: string(r.Name)}
		t.group, t.kind = backendRefGroupKind(r)
		if r.Namespace != nil {
			t.ns = string(*r.Namespace)
		}
		out = append(out, t)
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

func tlsRouteObjRefs(route *gwapiv1.TLSRoute) []gwapiv1.BackendObjectReference {
	var out []gwapiv1.BackendObjectReference
	for ri := range route.Spec.Rules {
		for bi := range route.Spec.Rules[ri].BackendRefs {
			out = append(out, route.Spec.Rules[ri].BackendRefs[bi].BackendObjectReference)
		}
	}
	return out
}

func tcpRouteObjRefs(route *gwapiv1.TCPRoute) []gwapiv1.BackendObjectReference {
	var out []gwapiv1.BackendObjectReference
	for ri := range route.Spec.Rules {
		for bi := range route.Spec.Rules[ri].BackendRefs {
			out = append(out, route.Spec.Rules[ri].BackendRefs[bi].BackendObjectReference)
		}
	}
	return out
}

// collectBackendTargets fans every attached route's backendRefs out into a
// single defaulted list, which the XBackend and Service resolvers each filter.
func collectBackendTargets(
	http []gwapiv1.HTTPRoute,
	grpc []gwapiv1.GRPCRoute,
	tls []gwapiv1.TLSRoute,
	tcp []gwapiv1.TCPRoute,
) []backendTarget {
	targets := make([]backendTarget, 0, len(http)+len(grpc)+len(tls)+len(tcp))
	for i := range http {
		targets = append(targets, targetsFromObjRefs(http[i].Namespace, "HTTPRoute", httpRouteObjRefs(&http[i]))...)
	}
	for i := range grpc {
		targets = append(targets, targetsFromObjRefs(grpc[i].Namespace, "GRPCRoute", grpcRouteObjRefs(&grpc[i]))...)
	}
	for i := range tls {
		targets = append(targets, targetsFromObjRefs(tls[i].Namespace, "TLSRoute", tlsRouteObjRefs(&tls[i]))...)
	}
	for i := range tcp {
		targets = append(targets, targetsFromObjRefs(tcp[i].Namespace, "TCPRoute", tcpRouteObjRefs(&tcp[i]))...)
	}
	return targets
}

// collectReferencedXBackends authorizes every XBackend target's cross-namespace
// reference via ReferenceGrant and fetches the permitted objects. The returned
// context is consumed by the resolver and by route/XBackend status patching.
func (r *GatewayReconciler) collectReferencedXBackends(ctx context.Context, targets []backendTarget) (*xbBackends, error) {
	col := &xbBackends{
		fetched:   map[xbKey]*apisxv1alpha1.XBackend{},
		permitted: map[permitKey]bool{},
	}

	for _, t := range targets {
		if !t.isXBackend() {
			continue
		}
		if t.routeNS != t.ns {
			pk := t.permit()
			if _, done := col.permitted[pk]; !done {
				ok, err := CheckReferenceGrantTo(ctx, r.Client, t.routeNS, t.routeKind, t.ns, xBackendGroup, xBackendKind, t.name)
				if err != nil {
					return nil, err
				}
				col.permitted[pk] = ok
			}
			if !col.permitted[pk] {
				continue
			}
		}

		key := xbKey{t.ns, t.name}
		if _, done := col.fetched[key]; done {
			continue
		}
		var xb apisxv1alpha1.XBackend
		if err := r.Client.Get(ctx, types.NamespacedName{Namespace: t.ns, Name: t.name}, &xb); err != nil {
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
func (r *GatewayReconciler) resolveXBackendRef(t backendTarget, col *xbBackends) (cfclient.ResolvedBackend, string) {
	if !r.ExperimentalBackends || col == nil {
		return cfclient.ResolvedBackend{}, reasonXBackendDisabled
	}
	if t.routeNS != t.ns && !col.permitted[t.permit()] {
		return cfclient.ResolvedBackend{}, reasonRefNotPermitted
	}
	xb := col.fetched[xbKey{t.ns, t.name}]
	if xb == nil {
		return cfclient.ResolvedBackend{}, reasonBackendNotFound
	}
	return translateXBackend(xb, t.routeKind)
}

// backendResolver returns the cloudflare BackendResolver used while building
// ingress rules. It claims every ref that must not reach the native Service URL
// builder — XBackend refs, unroutable kinds, and Service refs that are missing
// or unauthorized — so each becomes an error response instead of a live route.
// Permitted, existing Service refs are declined to the native path.
func (r *GatewayReconciler) backendResolver(xbCol *xbBackends, svcCol *svcBackends) cfclient.BackendResolver {
	return func(ref cfclient.BackendRef) (cfclient.ResolvedBackend, bool) {
		t := backendTarget{
			routeNS:   ref.RouteNamespace,
			routeKind: ref.RouteKind,
			group:     ref.Group,
			kind:      ref.Kind,
			ns:        ref.Namespace,
			name:      ref.Name,
		}
		switch {
		case t.isXBackend():
			rb, _ := r.resolveXBackendRef(t, xbCol)
			return rb, true
		case !t.isService():
			// An unroutable kind must not be silently treated as a Service.
			return cfclient.ResolvedBackend{}, true
		case resolveServiceRef(t, svcCol) != reasonResolvedOK:
			return cfclient.ResolvedBackend{}, true
		default:
			return cfclient.ResolvedBackend{}, false
		}
	}
}

// translateXBackend maps an XBackend spec to a Cloudflare tunnel service URL and
// the originRequest deltas its protocol/TLS settings imply. A non-empty reason
// means the backend can't be served.
//
// routeKind is the kind of route making the reference, or "" to judge the
// backend on its own. Some constraints are reference-level rather than
// intrinsic — a TCPRoute needs a tcp:// origin, a TLSRoute needs an encrypted
// one — so the same XBackend can be serviceable for one route kind and not
// another. Passing "" runs only the intrinsic checks, which is what the
// XBackend's own Accepted condition reports.
func translateXBackend(xb *apisxv1alpha1.XBackend, routeKind string) (cfclient.ResolvedBackend, string) {
	if xb.Spec.Type != apisxv1alpha1.BackendTypeExternalHostname || xb.Spec.ExternalHostname == nil {
		return cfclient.ResolvedBackend{}, reasonUnsupportedProtocol
	}
	host := string(xb.Spec.ExternalHostname.Hostname)
	port := int(xb.Spec.Port.Port)

	// Gateway API treats an unset protocol as "whatever the route or listener
	// determined", so a TCPRoute's backend defaults to TCP rather than HTTP.
	proto := apisxv1alpha1.BackendProtocolHTTP
	if routeKind == "TCPRoute" {
		proto = apisxv1alpha1.BackendProtocolTCP
	}
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
			if len(xb.Spec.TLS.Validation.SubjectAltNames) > 0 {
				return cfclient.ResolvedBackend{}, reasonUnsupportedTLSValidation
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

	// Reference-level: the origin scheme has to match the transport cloudflared
	// will actually be handed for this route kind.
	switch routeKind {
	case "HTTPRoute", "GRPCRoute":
		if scheme == "tcp" {
			return cfclient.ResolvedBackend{}, reasonIncompatibleRouteKind
		}
	case "TCPRoute":
		// A TCPRoute is an opaque byte stream; an http(s):// origin would have
		// cloudflared speak HTTP to a backend being sent raw TCP.
		if scheme != "tcp" {
			return cfclient.ResolvedBackend{}, reasonIncompatibleRouteKind
		}
	case "TLSRoute":
		// A TLSRoute hands cloudflared TLS bytes, so the origin must either
		// terminate TLS (https://) or take the stream unmodified (tcp://).
		if scheme == "http" {
			return cfclient.ResolvedBackend{}, reasonIncompatibleRouteKind
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
	case reasonXBackendDisabled:
		return 7
	case reasonUnsupportedKind:
		return 6
	case reasonRefNotPermitted:
		return 5
	case reasonBackendNotFound:
		return 4
	case reasonIncompatibleRouteKind:
		return 3
	case reasonUnsupportedProtocol:
		return 2
	case reasonUnsupportedCACerts, reasonUnsupportedTLSValidation:
		return 1
	default:
		return 0
	}
}

// resolveTargetReason reports why a single backendRef can't be served, or
// reasonResolvedOK when it can.
func (r *GatewayReconciler) resolveTargetReason(t backendTarget, xbCol *xbBackends, svcCol *svcBackends) string {
	switch {
	case t.isXBackend():
		_, reason := r.resolveXBackendRef(t, xbCol)
		return reason
	case t.isService():
		return resolveServiceRef(t, svcCol)
	default:
		return reasonUnsupportedKind
	}
}

// routeResolvedRefs computes a route's ResolvedRefs result across all of its
// backendRefs, reporting the most actionable failure.
func (r *GatewayReconciler) routeResolvedRefs(routeNS, routeKind string, objRefs []gwapiv1.BackendObjectReference, xbCol *xbBackends, svcCol *svcBackends) ResolvedRefsResult {
	worst := reasonResolvedOK
	for _, t := range targetsFromObjRefs(routeNS, routeKind, objRefs) {
		if reason := r.resolveTargetReason(t, xbCol, svcCol); reasonSeverity(reason) > reasonSeverity(worst) {
			worst = reason
		}
	}
	return resolvedRefsResultFor(worst)
}
