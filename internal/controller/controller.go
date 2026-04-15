package controller

import (
	"context"
	"fmt"

	cfv1alpha1 "github.com/mccormickt/cloudflared-gateway/api/v1alpha1"
	"github.com/mccormickt/cloudflared-gateway/internal/cloudflare"

	"k8s.io/apimachinery/pkg/fields"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

const (
	ControllerName    = "jan0ski.net/cloudflared-gateway"
	classGatewayIndex = "classGatewayIndex"
	finalizerName     = "cloudflared-gateway.jan0ski.net/cleanup"
)

// GatewayReconciler reconciles Gateway resources to create and manage Cloudflare Tunnels.
type GatewayReconciler struct {
	Client           client.Client
	CloudflareClient cloudflare.APIClient
	ControllerName   gwapiv1.GatewayController
}

var _ reconcile.Reconciler = &GatewayReconciler{}

// SetupWithManager registers the controller with the manager and configures watches.
// CloudflareClient and ControllerName must be set before calling this method.
func (r *GatewayReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.CloudflareClient == nil {
		return fmt.Errorf("CloudflareClient is required")
	}
	if r.ControllerName == "" {
		return fmt.Errorf("ControllerName is required")
	}
	r.Client = mgr.GetClient()

	// Set up field indexer for Gateway → GatewayClassName
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&gwapiv1.Gateway{},
		classGatewayIndex,
		func(rawObj client.Object) []string {
			gw := rawObj.(*gwapiv1.Gateway)
			return []string{string(gw.Spec.GatewayClassName)}
		},
	); err != nil {
		return fmt.Errorf("creating Gateway indexer: %w", err)
	}

	// Build controller with watches
	c := builder.ControllerManagedBy(mgr).
		Named("gatewayapi").
		For(&gwapiv1.Gateway{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&gwapiv1.GatewayClass{},
			handler.EnqueueRequestsFromMapFunc(r.gatewayClassToGateways),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&gwapiv1.HTTPRoute{},
			handler.EnqueueRequestsFromMapFunc(routeToGateways),
			builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	// TLSRoute watch is optional — CRD may not be installed
	c = c.Watches(&gwapiv1alpha2.TLSRoute{},
		handler.EnqueueRequestsFromMapFunc(routeToGateways),
		builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	// GRPCRoute watch — v1 stable
	c = c.Watches(&gwapiv1.GRPCRoute{},
		handler.EnqueueRequestsFromMapFunc(routeToGateways),
		builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	// TCPRoute watch is optional — CRD may not be installed
	c = c.Watches(&gwapiv1alpha2.TCPRoute{},
		handler.EnqueueRequestsFromMapFunc(routeToGateways),
		builder.WithPredicates(predicate.GenerationChangedPredicate{}))

	// BackendTLSPolicy watch — re-enqueue all Gateways on policy changes
	c = c.Watches(&gwapiv1.BackendTLSPolicy{},
		handler.EnqueueRequestsFromMapFunc(r.backendTLSPolicyToGateways))

	// CloudflareAccessPolicy watch — re-enqueue all Gateways on policy changes
	c = c.Watches(&cfv1alpha1.CloudflareAccessPolicy{},
		handler.EnqueueRequestsFromMapFunc(r.accessPolicyToGateways))

	if err := c.Complete(r); err != nil {
		return fmt.Errorf("building controller: %w", err)
	}
	return nil
}

// gatewayClassToGateways maps a GatewayClass change to all Gateways referencing it.
func (r *GatewayReconciler) gatewayClassToGateways(ctx context.Context, obj client.Object) []reconcile.Request {
	var gateways gwapiv1.GatewayList
	if err := r.Client.List(ctx, &gateways, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(classGatewayIndex, obj.GetName()),
	}); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list Gateways for GatewayClass", "gatewayClass", obj.GetName())
		return nil
	}

	requests := make([]reconcile.Request, 0, len(gateways.Items))
	for _, gw := range gateways.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&gw),
		})
	}
	return requests
}

// routeToGateways maps a route change to its parent Gateway(s).
func routeToGateways(_ context.Context, obj client.Object) []reconcile.Request {
	var parentRefs []gwapiv1.ParentReference

	switch route := obj.(type) {
	case *gwapiv1.HTTPRoute:
		parentRefs = route.Spec.ParentRefs
	case *gwapiv1.GRPCRoute:
		parentRefs = route.Spec.ParentRefs
	case *gwapiv1alpha2.TLSRoute:
		parentRefs = route.Spec.ParentRefs
	case *gwapiv1alpha2.TCPRoute:
		parentRefs = route.Spec.ParentRefs
	default:
		return nil
	}

	var requests []reconcile.Request
	for _, ref := range parentRefs {
		group := gwapiv1.GroupName
		if ref.Group != nil {
			group = string(*ref.Group)
		}
		kind := "Gateway"
		if ref.Kind != nil {
			kind = string(*ref.Kind)
		}

		if group != gwapiv1.GroupName || kind != "Gateway" {
			continue
		}

		ns := obj.GetNamespace()
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}

		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKey{
				Name:      string(ref.Name),
				Namespace: ns,
			},
		})
	}
	return requests
}

// backendTLSPolicyToGateways re-enqueues all Gateways when a BackendTLSPolicy
// changes. BackendTLSPolicy targets Services, not routes, so finding the exact
// affected Gateway requires traversing routes. Since policy changes are rare,
// we simply re-enqueue all Gateways.
func (r *GatewayReconciler) backendTLSPolicyToGateways(ctx context.Context, _ client.Object) []reconcile.Request {
	var gateways gwapiv1.GatewayList
	if err := r.Client.List(ctx, &gateways); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list Gateways for BackendTLSPolicy")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(gateways.Items))
	for _, gw := range gateways.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&gw),
		})
	}
	return requests
}

// accessPolicyToGateways re-enqueues all Gateways when a CloudflareAccessPolicy
// changes. Since policies use targetRefs (GEP-713 Policy Attachment) that can
// target Gateways or HTTPRoutes, determining the exact affected Gateway requires
// inspecting all policies. Policy changes are rare, so we re-enqueue all Gateways.
func (r *GatewayReconciler) accessPolicyToGateways(ctx context.Context, _ client.Object) []reconcile.Request {
	var gateways gwapiv1.GatewayList
	if err := r.Client.List(ctx, &gateways); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list Gateways for CloudflareAccessPolicy")
		return nil
	}

	requests := make([]reconcile.Request, 0, len(gateways.Items))
	for _, gw := range gateways.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: client.ObjectKeyFromObject(&gw),
		})
	}
	return requests
}
