package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// serviceKind is the Gateway API default backendRef kind, in the core group.
const serviceKind = "Service"

// svcKey identifies a Service backend.
type svcKey struct {
	namespace string
	name      string
}

// svcBackends is the per-reconcile resolution context for native Service
// backendRefs: exposed ports and which cross-namespace references a
// ReferenceGrant authorizes. A nil context resolves nothing, so every Service
// ref fails closed.
type svcBackends struct {
	ports     map[svcKey][]corev1.ServicePort
	permitted map[permitKey]bool
}

// collectServiceBackends resolves every native Service target: it authorizes
// cross-namespace references via ReferenceGrant and records exposed ports.
func (r *GatewayReconciler) collectServiceBackends(ctx context.Context, targets []backendTarget) (*svcBackends, error) {
	col := &svcBackends{
		ports:     map[svcKey][]corev1.ServicePort{},
		permitted: map[permitKey]bool{},
	}

	for _, t := range targets {
		if !t.isService() {
			continue
		}
		if t.routeNS != t.ns {
			pk := t.permit()
			if _, done := col.permitted[pk]; !done {
				ok, err := CheckReferenceGrant(ctx, r.Client, t.routeNS, t.routeKind, t.ns, serviceKind, t.name)
				if err != nil {
					return nil, err
				}
				col.permitted[pk] = ok
			}
			if !col.permitted[pk] {
				// An unauthorized ref is never dereferenced, so its existence is
				// deliberately not probed.
				continue
			}
		}

		key := svcKey{t.ns, t.name}
		if _, done := col.ports[key]; done {
			continue
		}
		var svc corev1.Service
		err := r.Client.Get(ctx, types.NamespacedName{Namespace: t.ns, Name: t.name}, &svc)
		switch {
		case err == nil:
			col.ports[key] = svc.Spec.Ports
		case apierrors.IsNotFound(err):
			col.ports[key] = nil
		default:
			return nil, err
		}
	}

	return col, nil
}

// resolveServiceRef reports why a native Service backendRef can't be served, or
// reasonResolvedOK when it can.
func resolveServiceRef(t backendTarget, col *svcBackends) string {
	if col == nil {
		return reasonBackendNotFound
	}
	if t.routeNS != t.ns && !col.permitted[t.permit()] {
		return reasonRefNotPermitted
	}
	for _, port := range col.ports[svcKey{t.ns, t.name}] {
		if int(port.Port) == t.port && (port.Protocol == "" || port.Protocol == corev1.ProtocolTCP) {
			return reasonResolvedOK
		}
	}
	return reasonBackendNotFound
}

// serviceToGateways maps Service existence and port changes to Gateways whose routes
// reference that Service, including secondary and cross-namespace references.
func (r *GatewayReconciler) serviceToGateways(ctx context.Context, svc client.Object) []reconcile.Request {
	seen := map[reconcile.Request]bool{}
	var requests []reconcile.Request
	add := func(route client.Object, kind string, refs []gwapiv1.BackendObjectReference) {
		for _, target := range targetsFromObjRefs(route.GetNamespace(), kind, refs) {
			if !target.isService() || target.ns != svc.GetNamespace() || target.name != svc.GetName() {
				continue
			}
			for _, request := range routeToGateways(ctx, route) {
				if !seen[request] {
					seen[request] = true
					requests = append(requests, request)
				}
			}
			break
		}
	}

	var httpRoutes gwapiv1.HTTPRouteList
	if err := r.Client.List(ctx, &httpRoutes); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list HTTPRoutes for Service watch mapping")
	} else {
		for i := range httpRoutes.Items {
			route := &httpRoutes.Items[i]
			add(route, "HTTPRoute", httpRouteObjRefs(route))
		}
	}
	var grpcRoutes gwapiv1.GRPCRouteList
	if err := r.Client.List(ctx, &grpcRoutes); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list GRPCRoutes for Service watch mapping")
	} else {
		for i := range grpcRoutes.Items {
			route := &grpcRoutes.Items[i]
			add(route, "GRPCRoute", grpcRouteObjRefs(route))
		}
	}
	var tlsRoutes gwapiv1.TLSRouteList
	if err := r.Client.List(ctx, &tlsRoutes); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list TLSRoutes for Service watch mapping")
	} else {
		for i := range tlsRoutes.Items {
			route := &tlsRoutes.Items[i]
			add(route, "TLSRoute", tlsRouteObjRefs(route))
		}
	}
	var tcpRoutes gwapiv1.TCPRouteList
	if err := r.Client.List(ctx, &tcpRoutes); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list TCPRoutes for Service watch mapping")
	} else {
		for i := range tcpRoutes.Items {
			route := &tcpRoutes.Items[i]
			add(route, "TCPRoute", tcpRouteObjRefs(route))
		}
	}
	return requests
}
