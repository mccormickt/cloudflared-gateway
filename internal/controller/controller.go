package controller

import (
	"context"
	"fmt"

	"github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare"

	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

const (
	ControllerName    = "jan0ski.net/cf-tunnel-controller"
	classGatewayIndex = "classGatewayIndex"
	finalizerName     = "cloudflare-tunnel-controller.jan0ski.net/cleanup"
)

type tunnelReconciler struct {
	client         client.Client
	cloudflare     cloudflare.APIClient
	controllerName gwapiv1.GatewayController
}

var _ reconcile.Reconciler = &tunnelReconciler{}

// NewGatewayAPIController creates a new Gateway API controller that reconciles Gateway resources
// to create and manage Cloudflare Tunnels.
func NewGatewayAPIController(mgr manager.Manager) error {
	ctx := context.Background()

	api, err := cloudflare.NewClientFromEnv()
	if err != nil {
		return fmt.Errorf("creating Cloudflare client: %w", err)
	}

	r := &tunnelReconciler{
		client:         mgr.GetClient(),
		cloudflare:     api,
		controllerName: gwapiv1.GatewayController(ControllerName),
	}

	c, err := controller.New("gatewayapi", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	if err = r.watchResources(ctx, mgr, c); err != nil {
		return err
	}
	return nil
}

// watchResources sets up watches for Gateway API resources.
func (r *tunnelReconciler) watchResources(ctx context.Context, mgr manager.Manager, c controller.Controller) error {
	// Primary: Gateway
	if err := c.Watch(
		source.Kind(mgr.GetCache(), &gwapiv1.Gateway{}),
		&handler.EnqueueRequestForObject{},
		predicate.GenerationChangedPredicate{},
	); err != nil {
		return fmt.Errorf("watching Gateway resources: %w", err)
	}

	// Secondary: GatewayClass → map to Gateways
	if err := c.Watch(
		source.Kind(mgr.GetCache(), &gwapiv1.GatewayClass{}),
		handler.EnqueueRequestsFromMapFunc(r.gatewayClassToGateways),
		predicate.GenerationChangedPredicate{},
	); err != nil {
		return fmt.Errorf("watching GatewayClass resources: %w", err)
	}

	// Secondary: HTTPRoute → map to parent Gateway
	if err := c.Watch(
		source.Kind(mgr.GetCache(), &gwapiv1.HTTPRoute{}),
		handler.EnqueueRequestsFromMapFunc(routeToGateways),
	); err != nil {
		return fmt.Errorf("watching HTTPRoute resources: %w", err)
	}

	// Secondary: TLSRoute → map to parent Gateway (optional CRD)
	if err := c.Watch(
		source.Kind(mgr.GetCache(), &gwapiv1alpha2.TLSRoute{}),
		handler.EnqueueRequestsFromMapFunc(routeToGateways),
	); err != nil {
		// TLSRoute CRD may not be installed — log and continue
		mgr.GetLogger().Info("TLSRoute watch not configured (CRD may not be installed)", "error", err)
	}

	// Field indexer: Gateway → GatewayClassName
	if err := mgr.GetFieldIndexer().IndexField(
		ctx,
		&gwapiv1.Gateway{},
		classGatewayIndex,
		func(rawObj client.Object) []string {
			gw := rawObj.(*gwapiv1.Gateway)
			return []string{string(gw.Spec.GatewayClassName)}
		},
	); err != nil {
		return fmt.Errorf("creating Gateway indexer: %w", err)
	}

	return nil
}

// gatewayClassToGateways maps a GatewayClass change to all Gateways referencing it.
func (r *tunnelReconciler) gatewayClassToGateways(ctx context.Context, obj client.Object) []reconcile.Request {
	var gateways gwapiv1.GatewayList
	if err := r.client.List(ctx, &gateways, &client.ListOptions{
		FieldSelector: fields.OneTermEqualSelector(classGatewayIndex, obj.GetName()),
	}); err != nil {
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
	case *gwapiv1alpha2.TLSRoute:
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
