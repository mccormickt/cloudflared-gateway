package controller

import (
	"context"
	"errors"
	"fmt"

	cf "github.com/cloudflare/cloudflare-go"
	cfclient "github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func (r *tunnelReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	logger := log.FromContext(ctx).WithValues("gateway", req.NamespacedName)
	ctx = log.IntoContext(ctx, logger)
	logger.Info("Reconciling Gateway")

	// 1. Fetch Gateway
	var gw gwapiv1.Gateway
	if err := r.client.Get(ctx, req.NamespacedName, &gw); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("fetching Gateway: %w", err)
	}

	// 2. Validate GatewayClass — must happen before finalizer to avoid claiming other controllers' Gateways
	var gc gwapiv1.GatewayClass
	if err := r.client.Get(ctx, types.NamespacedName{Name: string(gw.Spec.GatewayClassName)}, &gc); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("fetching GatewayClass: %w", err)
	}
	if gc.Spec.ControllerName != r.controllerName {
		return reconcile.Result{}, nil
	}

	// 3. Finalizer handling
	if !gw.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&gw, finalizerName) {
			if err := r.cleanup(ctx, &gw); err != nil {
				logger.Error(err, "Cleanup failed")
			}
			controllerutil.RemoveFinalizer(&gw, finalizerName)
			if err := r.client.Update(ctx, &gw); err != nil {
				return reconcile.Result{}, fmt.Errorf("removing finalizer: %w", err)
			}
		}
		return reconcile.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(&gw, finalizerName) {
		controllerutil.AddFinalizer(&gw, finalizerName)
		if err := r.client.Update(ctx, &gw); err != nil {
			return reconcile.Result{}, fmt.Errorf("adding finalizer: %w", err)
		}
	}

	// 4. Apply
	if err := r.apply(ctx, &gw, &gc); err != nil {
		return reconcile.Result{}, err
	}

	return reconcile.Result{}, nil
}

func (r *tunnelReconciler) apply(ctx context.Context, gw *gwapiv1.Gateway, gc *gwapiv1.GatewayClass) error {
	logger := log.FromContext(ctx)

	// Set GatewayClass Accepted status
	if err := PatchGatewayClassStatus(ctx, r.client, gc, true); err != nil {
		logger.Error(err, "Failed to patch GatewayClass status")
	}

	// Ensure tunnel secret
	secret, regenerated, err := EnsureTunnelSecret(ctx, r.client, gw)
	if err != nil {
		return fmt.Errorf("ensuring tunnel secret: %w", err)
	}

	// Get or create tunnel
	tunnel, err := r.cloudflare.GetTunnelByName(ctx, gw.Name)
	if err != nil {
		return fmt.Errorf("getting tunnel: %w", err)
	}

	if tunnel != nil && regenerated {
		// Secret was regenerated — delete and recreate tunnel
		logger.Info("Secret regenerated, recreating tunnel", "tunnel_id", tunnel.ID)
		if err := r.cloudflare.DeleteTunnel(ctx, tunnel.ID); err != nil {
			return fmt.Errorf("deleting tunnel for recreation: %w", err)
		}
		tunnel = nil
	}

	if tunnel == nil {
		created, err := r.cloudflare.CreateTunnel(ctx, gw.Name, secret)
		if err != nil {
			return fmt.Errorf("creating tunnel: %w", err)
		}
		tunnel = &created
		logger.Info("Created tunnel", "tunnel_id", tunnel.ID)
	}

	// Build and store tunnel token
	token := cfclient.BuildTunnelToken(r.cloudflare.AccountID(), tunnel.ID, secret)
	secretName := TunnelSecretName(gw.Name)
	if err := StoreTunnelToken(ctx, r.client, gw.Namespace, secretName, token); err != nil {
		return fmt.Errorf("storing tunnel token: %w", err)
	}

	// Apply cloudflared deployment
	deployment := BuildCloudflaredDeployment(gw, secretName)
	if err := r.applyDeployment(ctx, deployment); err != nil {
		return fmt.Errorf("applying deployment: %w", err)
	}

	// Collect attached routes
	httpRoutes, err := r.collectHTTPRoutes(ctx, gw)
	if err != nil {
		return fmt.Errorf("collecting HTTPRoutes: %w", err)
	}

	tlsRoutes, err := r.collectTLSRoutes(ctx, gw)
	if err != nil {
		return fmt.Errorf("collecting TLSRoutes: %w", err)
	}

	// Build ingress rules
	var ingress []cf.UnvalidatedIngressRule
	ingress = append(ingress, cfclient.BuildIngressRules(httpRoutes)...)
	ingress = append(ingress, cfclient.BuildTLSIngressRules(tlsRoutes)...)
	// Catch-all 404
	ingress = append(ingress, cf.UnvalidatedIngressRule{Service: "http_status:404"})

	// Push config
	if err := r.cloudflare.UpdateTunnelConfiguration(ctx, tunnel.ID, ingress); err != nil {
		return fmt.Errorf("updating tunnel config: %w", err)
	}
	logger.Info("Pushed tunnel config", "rules", len(ingress))

	// Set route statuses
	for i := range httpRoutes {
		if err := PatchHTTPRouteStatus(ctx, r.client, &httpRoutes[i], gw.Name, gw.Namespace, true); err != nil {
			logger.Error(err, "Failed to patch HTTPRoute status", "route", httpRoutes[i].Name)
		}
	}
	for i := range tlsRoutes {
		if err := PatchTLSRouteStatus(ctx, r.client, &tlsRoutes[i], gw.Name, gw.Namespace, true); err != nil {
			logger.Error(err, "Failed to patch TLSRoute status", "route", tlsRoutes[i].Name)
		}
	}

	// Compute listener route counts and set Gateway status
	listenerCounts := computeListenerCounts(gw, httpRoutes, tlsRoutes)
	if err := PatchGatewayStatus(ctx, r.client, gw, tunnel.ID, listenerCounts); err != nil {
		logger.Error(err, "Failed to patch Gateway status")
	}

	return nil
}

func (r *tunnelReconciler) cleanup(ctx context.Context, gw *gwapiv1.Gateway) error {
	logger := log.FromContext(ctx)
	var firstErr error

	// Delete tunnel
	tunnel, err := r.cloudflare.GetTunnelByName(ctx, gw.Name)
	if err != nil {
		logger.Error(err, "Cleanup: failed to get tunnel")
		if firstErr == nil {
			firstErr = err
		}
	} else if tunnel != nil {
		if err := r.cloudflare.DeleteTunnel(ctx, tunnel.ID); err != nil {
			logger.Error(err, "Cleanup: failed to delete tunnel")
			if firstErr == nil {
				firstErr = err
			}
		} else {
			logger.Info("Cleanup: deleted tunnel", "tunnel_id", tunnel.ID)
		}
	}

	// Delete deployment
	deployName := DeploymentName(gw.Name)
	var deploy appsv1.Deployment
	if err := r.client.Get(ctx, types.NamespacedName{Name: deployName, Namespace: gw.Namespace}, &deploy); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Cleanup: failed to get deployment")
			if firstErr == nil {
				firstErr = err
			}
		}
	} else {
		if err := r.client.Delete(ctx, &deploy); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Cleanup: failed to delete deployment")
			if firstErr == nil {
				firstErr = err
			}
		} else {
			logger.Info("Cleanup: deleted deployment", "name", deployName)
		}
	}

	// Delete secret
	secretName := TunnelSecretName(gw.Name)
	var secret v1.Secret
	if err := r.client.Get(ctx, types.NamespacedName{Name: secretName, Namespace: gw.Namespace}, &secret); err != nil {
		if !apierrors.IsNotFound(err) {
			logger.Error(err, "Cleanup: failed to get secret")
			if firstErr == nil {
				firstErr = err
			}
		}
	} else {
		if err := r.client.Delete(ctx, &secret); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "Cleanup: failed to delete secret")
			if firstErr == nil {
				firstErr = err
			}
		} else {
			logger.Info("Cleanup: deleted secret", "name", secretName)
		}
	}

	return firstErr
}

func (r *tunnelReconciler) applyDeployment(ctx context.Context, desired *appsv1.Deployment) error {
	var existing appsv1.Deployment
	err := r.client.Get(ctx, types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}, &existing)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return r.client.Create(ctx, desired)
		}
		return err
	}

	// Update existing deployment
	existing.Spec = desired.Spec
	existing.Labels = desired.Labels
	return r.client.Update(ctx, &existing)
}

func (r *tunnelReconciler) collectHTTPRoutes(ctx context.Context, gw *gwapiv1.Gateway) ([]gwapiv1.HTTPRoute, error) {
	var routeList gwapiv1.HTTPRouteList
	if err := r.client.List(ctx, &routeList); err != nil {
		return nil, err
	}

	var attached []gwapiv1.HTTPRoute
	for _, route := range routeList.Items {
		if !routeReferencesGateway(route.Spec.ParentRefs, gw) {
			continue
		}
		if !CheckRouteAttachment(gw, route.Namespace, "HTTPRoute") {
			continue
		}
		attached = append(attached, route)
	}
	return attached, nil
}

func (r *tunnelReconciler) collectTLSRoutes(ctx context.Context, gw *gwapiv1.Gateway) ([]gwapiv1alpha2.TLSRoute, error) {
	var routeList gwapiv1alpha2.TLSRouteList
	if err := r.client.List(ctx, &routeList); err != nil {
		if apierrors.IsNotFound(err) || isNoMatchError(err) {
			// TLSRoute CRD not installed
			return nil, nil
		}
		return nil, err
	}

	var attached []gwapiv1alpha2.TLSRoute
	for _, route := range routeList.Items {
		if !routeReferencesGateway(route.Spec.ParentRefs, gw) {
			continue
		}
		if !CheckRouteAttachment(gw, route.Namespace, "TLSRoute") {
			continue
		}
		attached = append(attached, route)
	}
	return attached, nil
}

func routeReferencesGateway(parentRefs []gwapiv1.ParentReference, gw *gwapiv1.Gateway) bool {
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

		ns := gw.Namespace
		if ref.Namespace != nil {
			ns = string(*ref.Namespace)
		}

		if string(ref.Name) == gw.Name && ns == gw.Namespace {
			return true
		}
	}
	return false
}

func computeListenerCounts(gw *gwapiv1.Gateway, httpRoutes []gwapiv1.HTTPRoute, tlsRoutes []gwapiv1alpha2.TLSRoute) []ListenerRouteCount {
	counts := make([]ListenerRouteCount, 0, len(gw.Spec.Listeners))
	for _, listener := range gw.Spec.Listeners {
		var count int32
		switch listener.Protocol {
		case gwapiv1.HTTPProtocolType, gwapiv1.HTTPSProtocolType:
			count = int32(len(httpRoutes))
			if listener.Protocol == gwapiv1.HTTPSProtocolType {
				count += int32(len(tlsRoutes))
			}
		case gwapiv1.TLSProtocolType:
			count = int32(len(tlsRoutes))
		}
		counts = append(counts, ListenerRouteCount{
			Name:     listener.Name,
			Protocol: listener.Protocol,
			Count:    count,
		})
	}
	return counts
}

// isNoMatchError checks if the error is a "no matches for kind" API discovery error,
// indicating the CRD is not installed.
func isNoMatchError(err error) bool {
	if err == nil {
		return false
	}
	var noMatch *meta.NoKindMatchError
	return apierrors.IsNotFound(err) || errors.As(err, &noMatch)
}
