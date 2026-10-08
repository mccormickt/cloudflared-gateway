# cloudflared-gateway

Helm chart for the `cloudflared-gateway` Kubernetes controller — a Gateway API
implementation that provisions Cloudflare Tunnels from `Gateway`, `HTTPRoute`,
`GRPCRoute`, `TLSRoute`, and `TCPRoute` resources.

## Prerequisites

The controller requires the standard Gateway API v1.6.3 CRDs (`v1` Gateway,
GatewayClass, HTTPRoute, GRPCRoute, TLSRoute, TCPRoute, BackendTLSPolicy, and
ReferenceGrant). Older bundles such as v1.5.x lack required `v1` APIs. Install
them before the chart unless the cluster already has a compatible bundle:

```sh
kubectl apply --server-side -f \
  https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.3/standard-install.yaml
```

Alternatively, on a new cluster with no existing Gateway API bundle, opt in to
the [CRD install hook](#optional-gateway-api-crd-install-hook) by adding
`--set experimental.installGatewayAPICRDs=true` to `helm install`. The hook
requires Kubernetes >=1.30 and egress from the release namespace to
`github.com`, `raw.githubusercontent.com`, and GitHub's release-asset CDN (for
example `release-assets.githubusercontent.com`).

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
| `experimental.backends.enabled` | bool | `false` | Enable XBackend support. Requires a served `gateway.networking.x-k8s.io/v1alpha1` CRD from a GA v1.6.x bundle. |
| `experimental.installGatewayAPICRDs` | bool | `false` | Install standard Gateway API CRDs, plus XBackend if enabled. Requires Kubernetes >=1.30 and egress to GitHub (see below). Keep disabled for existing shared, manually installed, or experimental bundles. |
| `experimental.gatewayAPIVersion` | string | `v1.6.3` | Release installed by the hook. Validated only when the hook is enabled: must be a GA v1 release >=v1.6.0, and a GA v1.6.x release when installing XBackend. |
| `experimental.crdInstaller.image` | string | `registry.k8s.io/kubectl:v1.34.1` | Hook image containing kubectl. |
| `experimental.crdInstaller.forceConflicts` | bool | `false` | Allow the hook to take field ownership from another manager. Only for a deliberate, administrator-approved ownership transfer. |

Gateway API v1.6.3 has no stable XBackend API. For installation instructions and
the startup/CRD compatibility matrix, see the
[external origins guide](../../README.md#experimental-external-origins-xbackend).

## Optional Gateway API CRD install hook

With `experimental.installGatewayAPICRDs=true`, a pre-install/pre-upgrade Job
server-side applies the standard bundle (plus only the XBackend CRD when
`experimental.backends.enabled=true`) with the field manager
`<fullname>-crd-install` (fullname truncated to 51 characters so the name
fits in 63). It needs Kubernetes >=1.30 and egress to
`github.com`, `raw.githubusercontent.com`, and GitHub's release-asset CDN (for
example `release-assets.githubusercontent.com`). The hook pod reuses
`imagePullSecrets`, `nodeSelector`, `tolerations`, `affinity.nodeAffinity`,
`podSecurityContext`, `securityContext`, and `resources`; it is labelled
`app.kubernetes.io/name: <name>-crd-install`, so controller-targeted
NetworkPolicies and PodDisruptionBudgets do not select it. Allow its egress
explicitly if a default-deny policy applies.

**Ownership.** CRDs and admission policies are shared cluster-wide resources,
are not owned by the Helm release, and Helm rollback does not undo changes to
them. The hook can upgrade without ownership conflicts only bundles that this
hook applied earlier under the same release name and fullname. A manually
installed bundle, a different release name, or a changed `fullnameOverride`
leaves the previous server-side-apply owner in place, so applying a different
version conflicts. In those cases, keep the hook disabled and upgrade the CRDs
out of band, or have a cluster administrator explicitly approve the ownership
transfer and set `experimental.crdInstaller.forceConflicts=true` as a one-off,
deliberate action. The chart never adopts existing bundles silently.

**Failed hooks.** Successful hook resources are deleted automatically. A failed
Job, its ServiceAccount, ClusterRole, and ClusterRoleBinding remain (including
after `helm uninstall` or disabling the hook) until the next hook run replaces
them. Inspect and clean up with:

```sh
kubectl -n cloudflared-gateway logs job/<fullname>-crd-install
kubectl -n cloudflared-gateway delete job,serviceaccount <fullname>-crd-install
kubectl delete clusterrole,clusterrolebinding <fullname>-crd-install
```

## Upgrading

Helm installs CRDs from the chart's `crds/` directory on first install but
**does not** upgrade or delete them on `helm upgrade`/`helm uninstall`. This is
by design — see the
[Helm docs on CRDs](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/).

To pick up CRD changes shipped in a new chart version, apply them manually
before upgrading:

```sh
kubectl apply -f https://raw.githubusercontent.com/mccormickt/cloudflared-gateway/main/charts/cloudflared-gateway/crds/cloudflareaccesspolicy.yaml
kubectl apply -f https://raw.githubusercontent.com/mccormickt/cloudflared-gateway/main/charts/cloudflared-gateway/crds/cloudflareoriginpolicy.yaml
kubectl apply -f https://raw.githubusercontent.com/mccormickt/cloudflared-gateway/main/charts/cloudflared-gateway/crds/cloudflaretunnelconfig.yaml
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
kubectl delete crd cloudflareaccesspolicies.cloudflare.jan0ski.net \
  cloudflareoriginpolicies.cloudflare.jan0ski.net \
  cloudflaretunnelconfigs.cloudflare.jan0ski.net
```

Deleting these CRDs deletes every corresponding custom resource in the cluster.
Gateway API CRDs installed by the optional hook also remain after uninstall;
manage them separately with the cluster administrator. Resources from a failed
hook run also remain; see [Failed hooks](#optional-gateway-api-crd-install-hook).
