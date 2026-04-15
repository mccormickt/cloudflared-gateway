# cloudflared-gateway

Helm chart for the `cloudflared-gateway` Kubernetes controller — a Gateway API
implementation that provisions Cloudflare Tunnels from `Gateway`, `HTTPRoute`,
`GRPCRoute`, `TLSRoute`, and `TCPRoute` resources.

## Install

The chart is published as an OCI artifact. Install directly from the registry:

```sh
helm install cloudflared-gateway oci://ghcr.io/mccormickt/charts/cloudflared-gateway \
  --version 0.1.0 \
  --create-namespace --namespace cloudflared-gateway \
  --set cloudflare.existingSecret=cloudflare-creds
```

Or provide credentials inline (a Secret will be created for you):

```sh
helm install cloudflared-gateway oci://ghcr.io/mccormickt/charts/cloudflared-gateway \
  --version 0.1.0 \
  --create-namespace --namespace cloudflared-gateway \
  --set cloudflare.accountId=<account-id> \
  --set cloudflare.apiToken=<api-token>
```

### Pre-existing credentials Secret

If you set `cloudflare.existingSecret`, the Secret must live in the release
namespace and contain these keys:

| Key          | Value                   |
|--------------|-------------------------|
| `account-id` | Cloudflare account ID   |
| `api-token`  | Cloudflare API token    |

Example:

```sh
kubectl -n cloudflared-gateway create secret generic cloudflare-creds \
  --from-literal=account-id=<account-id> \
  --from-literal=api-token=<api-token>
```

## Values reference

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `image.repository` | string | `ghcr.io/mccormickt/cloudflared-gateway` | Controller image repository. |
| `image.tag` | string | `""` | Image tag. Defaults to `.Chart.AppVersion` when empty. |
| `image.pullPolicy` | string | `IfNotPresent` | Image pull policy. |
| `imagePullSecrets` | list | `[]` | References to Secrets used for pulling the controller image. |
| `replicaCount` | int | `1` | Number of controller replicas. |
| `controllerName` | string | `jan0ski.net/cloudflared-gateway` | The GatewayClass `controllerName` value this controller claims. |
| `cloudflare.accountId` | string | `""` | Cloudflare account ID. Rendered into a Secret when `existingSecret` is empty. |
| `cloudflare.apiToken` | string | `""` | Cloudflare API token. Rendered into a Secret when `existingSecret` is empty. |
| `cloudflare.existingSecret` | string | `""` | Name of a pre-existing Secret in the release namespace with keys `account-id` and `api-token`. When set, no Secret is created. |
| `serviceAccount.create` | bool | `true` | Whether to create a ServiceAccount. |
| `serviceAccount.name` | string | `""` | ServiceAccount name. Defaults to the release fullname when empty. |
| `serviceAccount.annotations` | object | `{}` | Annotations added to the ServiceAccount. |
| `rbac.create` | bool | `true` | Whether to create the ClusterRole and ClusterRoleBinding. |
| `podSecurityContext` | object | see `values.yaml` | Pod-level security context. |
| `securityContext` | object | see `values.yaml` | Container-level security context. |
| `resources` | object | see `values.yaml` | Container resource requests and limits. |
| `nodeSelector` | object | `{}` | Node selector for the controller pod. |
| `tolerations` | list | `[]` | Tolerations for the controller pod. |
| `affinity` | object | `{}` | Affinity rules for the controller pod. |
| `podAnnotations` | object | `{}` | Annotations added to the controller pod. |
| `podLabels` | object | `{}` | Additional labels added to the controller pod. |
| `extraEnv` | list | `[]` | Extra environment variables merged into the `manager` container after the Cloudflare credential vars. |

## Upgrading

Helm installs CRDs from the chart's `crds/` directory on first install but
**does not** upgrade or delete them on `helm upgrade`/`helm uninstall`. This is
by design — see the
[Helm docs on CRDs](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/).

To pick up CRD changes shipped in a new chart version, apply them manually
before upgrading:

```sh
kubectl apply -f https://raw.githubusercontent.com/mccormickt/cloudflared-gateway/main/charts/cloudflared-gateway/crds/cloudflareaccesspolicy.yaml
helm upgrade cloudflared-gateway oci://ghcr.io/mccormickt/charts/cloudflared-gateway \
  --version <new-version> \
  --namespace cloudflared-gateway
```

If you have a local checkout:

```sh
kubectl apply -f charts/cloudflared-gateway/crds/
helm upgrade cloudflared-gateway charts/cloudflared-gateway \
  --namespace cloudflared-gateway
```

## Uninstalling

```sh
helm uninstall cloudflared-gateway --namespace cloudflared-gateway
```

This removes the Deployment, ServiceAccount, ClusterRole, ClusterRoleBinding,
and (if it was chart-managed) the Cloudflare credentials Secret. CRDs are left
in place. Delete them explicitly if you want them gone:

```sh
kubectl delete crd cloudflareaccesspolicies.cloudflare.jan0ski.net
```

Deleting the CRD cascades to every `CloudflareAccessPolicy` in the cluster.
