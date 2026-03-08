package controller

import (
	"context"
	"fmt"
	"slices"

	"github.com/mccormickt/cloudflare-tunnel-controller/internal/cloudflare"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	ControllerName        = "jan0ski.net/cf-tunnel-controller"
	gatewayClassFinalizer = gwapiv1.GatewayClassFinalizerGatewaysExist
	classGatewayIndex     = "classGatewayIndex"
)

type tunnelReconciler struct {
	client              client.Client
	cloudflare          cloudflare.APIClient
	cloudflareAccountID string
	classController     gwapiv1.GatewayController
	namespace           string
}

var _ reconcile.Reconciler = &tunnelReconciler{}

// newGatewayAPIController creates a new Gateway API controller that reconciles Gateway resources
// to create and manage Cloudflare Tunnels
func NewGatewayAPIController(mgr manager.Manager, namespace string) error {
	ctx := context.Background()

	api := cloudflare.NewClientFromEnv()
	if api == nil {
		return fmt.Errorf("error creating Cloudflare API client")
	}

	r := &tunnelReconciler{
		client:          mgr.GetClient(),
		cloudflare:      api,
		classController: gwapiv1.GatewayController(ControllerName),
		namespace:       namespace,
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

// / Reconcile reconciles the Gateway resources
func (r *tunnelReconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	log := log.FromContext(ctx)
	log.Info("Reconciling Gateways")

	// Fetch GatewayClass instance relevant to the controller
	var gatewayClasses gwapiv1.GatewayClassList
	if err := r.client.List(ctx, &gatewayClasses, &client.ListOptions{}); err != nil {
		return reconcile.Result{}, fmt.Errorf("error listing gatewayclasses: %w", err)
	}
	log.Info("Fetched GatewayClasses", "count", len(gatewayClasses.Items))

	cc := make(map[string]*gwapiv1.GatewayClass)
	for _, gwClass := range gatewayClasses.Items {
		gwClass := gwClass
		if gwClass.Spec.ControllerName == r.classController {
			// The gatewayclass was marked for deletion and the finalizer removed,
			// so clean-up dependents.
			if !gwClass.DeletionTimestamp.IsZero() && !slices.Contains[[]string](gwClass.Finalizers, gatewayClassFinalizer) {
				log.Info("gatewayclass marked for deletion")
				delete(cc, gwClass.Name)
			} else {
				cc[gwClass.Name] = &gwClass
			}
		}
	}

	if len(cc) == 0 {
		log.Info("No GatewayClass found for controller")
		return reconcile.Result{}, nil
	}

	for _, gwClass := range cc {
		acceptedGC := gwClass

		if err := r.processGateways(ctx, acceptedGC); err != nil {
			return reconcile.Result{}, fmt.Errorf("error processing gateways: %w", err)
		}
	}

	return reconcile.Result{}, nil

}

func (r *tunnelReconciler) processGateways(ctx context.Context, gwClass *gwapiv1.GatewayClass) error {
	log := log.FromContext(ctx)
	log.Info("Processing Gateways for GatewayClass", "gatewayclass", gwClass.Name)

	var gateways gwapiv1.GatewayList
	if err := r.client.List(ctx, &gateways, &client.ListOptions{FieldSelector: fields.OneTermEqualSelector(classGatewayIndex, gwClass.Name)}); err != nil {
		log.Info("No associated Gateways found for GatewayClass", "name", gwClass.Name)
		return err
	}

	for _, gw := range gateways.Items {
		gw := gw
		log.Info("Processing Gateway", "name", gw.Name, "namespace", gw.Namespace)

		tunnel, err := r.cloudflare.GetTunnel(ctx, gw.Name)
		if err == nil {
			log.Info("Tunnel already exists", "name", tunnel.Name, "id", tunnel.ID)
			continue
		}

		tunnel, err = r.cloudflare.CreateTunnel(ctx, gw.Name)
		if err != nil {
			return err
		}
		log.Info("Created Tunnel", "name", tunnel.Name, "id", tunnel.ID)

		// Deploy cloudflared to the Gateway's namespace
		if err := r.deployCloudflared(ctx, gw.Namespace); err != nil {
			return fmt.Errorf("error deploying cloudflared: %w", err)
		}

		// Update Gateway status details on the Cloudflare Tunnel Gateway resource
		gw.Status = gwapiv1.GatewayStatus{
			Addresses: []gwapiv1.GatewayStatusAddress{},
			Listeners: []gwapiv1.ListenerStatus{},
		}

	}

	return nil
}

// watchResources initializes the watch and indexes for GatewayAPI resources
func (r *tunnelReconciler) watchResources(ctx context.Context, mgr manager.Manager, c controller.Controller) error {
	if err := c.Watch(
		source.Kind(mgr.GetCache(), &gwapiv1.GatewayClass{}),
		&handler.EnqueueRequestForObject{},
		predicate.GenerationChangedPredicate{},
	); err != nil {
		return fmt.Errorf("Unable to watch GatewayClass resources %v", err)
	}

	if err := c.Watch(
		source.Kind(mgr.GetCache(), &gwapiv1.Gateway{}),
		&handler.EnqueueRequestForObject{},
	); err != nil {
		return fmt.Errorf("Unable to watch Gateway resources %v", err)
	}

	// Create indexer for Gateway resources based on GatewayClass
	if err := mgr.GetFieldIndexer().
		IndexField(
			ctx,
			&gwapiv1.Gateway{},
			classGatewayIndex,
			func(rawObj client.Object) []string {
				gateway := rawObj.(*gwapiv1.Gateway)
				return []string{string(gateway.Spec.GatewayClassName)}
			},
		); err != nil {
		return fmt.Errorf("Unable to create indexer for Gateway resources %v", err)
	}
	return nil
}

// deployCloudflared creates a cloudflared deployment in the Gateway's namespace
func (r *tunnelReconciler) deployCloudflared(ctx context.Context, namespace string) error {
	// Deploy cloudflared to the Gateway's namespace
	var replicas int32 = 2
	return r.client.Create(ctx,
		&appsv1.Deployment{
			TypeMeta: metav1.TypeMeta{},
			ObjectMeta: metav1.ObjectMeta{
				Name:            "cloudflared-gateway",
				Namespace:       namespace,
				Labels:          map[string]string{},
				Annotations:     map[string]string{},
				OwnerReferences: []metav1.OwnerReference{},
			},
			Spec: appsv1.DeploymentSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": "cloudflared-gateway",
					},
				},
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": "cloudflared-gateway",
						},
					},
					Spec: v1.PodSpec{
						Containers: []v1.Container{
							{
								Name:  "cloudflared",
								Image: cloudflare.ContainerImage,
								Args:  []string{"tunnel", "--metrics", "0.0.0.0:2000", "run"},
								Env: []v1.EnvVar{
									{Name: "TUNNEL_TOKEN", Value: cloudflare.TunnelSecret},
								},
								Ports: []v1.ContainerPort{{Name: "metrics", ContainerPort: 2000}},
							},
						},
					},
				},
				Strategy: appsv1.DeploymentStrategy{
					Type: "RollingUpdate",
					RollingUpdate: &appsv1.RollingUpdateDeployment{
						MaxUnavailable: &intstr.IntOrString{
							Type:   intstr.String,
							StrVal: "50%",
						},
						MaxSurge: &intstr.IntOrString{
							Type:   intstr.String,
							StrVal: "100%",
						},
					},
				},
			},
		},
	)
}
