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
| `dns.enabled` | bool | `false` | Manage proxied CNAME records for attached route hostnames. Requires Zone:Read and DNS:Edit. Leave disabled when ExternalDNS owns these records. |
| `gatewayAPI.enabled` | bool | `false` | Bundle Gateway API v1.7.0 experimental CRDs and safe-upgrade policies. Requires Kubernetes 1.32+. Uses the installer's permissions, not pod RBAC. |
| `experimental.backends.enabled` | bool | `false` | Enable XBackend support. Requires Kubernetes 1.32+ and the v1.7.x XBackend CRD; the stable CRDs can remain on the standard channel. |

## Gateway API ownership

**Administrator-managed (default):** Install the required Gateway API CRDs before
the controller and leave `gatewayAPI.enabled=false`. This keeps shared CRDs and
admission policies outside the application release.

**Fresh cluster:** Add `--set gatewayAPI.enabled=true` to the Helm install. The
vendored dependency installs the v1.7.0 experimental CRDs and upstream
safe-upgrade policy/binding. The Helm caller needs cluster permissions to install
these resources; neither the controller nor an installer pod receives CRD or
admission-policy write permissions. No installation Job runs.

For **Helm 4**, also pass `--server-side=false` on install (including
`upgrade --install` when the release is absent). Helm 4's default SSA path
attempts to apply existing CRDs; it can conflict with their owner or change
unowned fields. With this flag, Helm uses Helm 3's create-and-skip behavior and
leaves existing CRD versions/channels unchanged. Helm 3 needs no extra flag.

The chart renders the safe-upgrade policy and binding only when neither is
externally owned: each must be absent or owned by this release. Both resources use
`helm.sh/resource-policy: keep`, so disabling the dependency or uninstalling the
controller does not remove shared upgrade protections. Removing them is an
explicit administrator action. The bundle version is pinned to the controller's
Gateway API module; it is not a runtime value.

For Argo CD or other offline Helm rendering, `lookup` cannot inspect the cluster.
Use a separate infrastructure application to manage shared Gateway API resources
and leave `gatewayAPI.enabled=false` in the controller application. Restrict the
application's GitOps project and execution identity so it cannot modify CRDs or
admission policies. `--skip-crds` alone is not sufficient: it does not omit policy
templates.

The `experimental.installGatewayAPICRDs`, `experimental.gatewayAPIVersion`, and
`experimental.crdInstaller` values are removed. Replace the install flag with
`gatewayAPI.enabled=true` for fresh clusters, or install the shared bundle
separately. Remove any failed installer hook resources from earlier installs
after checking that no installer Job is running; Helm does not manage those
hook resources as part of the release.

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
kubectl apply --server-side --field-manager=helm -f charts/cloudflared-gateway/crds/
# Cluster administrator: review and apply the Gateway API CRDs before the controller.
kubectl apply --server-side --field-manager=helm -f charts/cloudflared-gateway/charts/gateway-api-crds/crds/
helm upgrade cloudflared-gateway charts/cloudflared-gateway \
  --namespace cloudflared-gateway
```

These commands use the `helm` field manager for Helm-installed CRDs. If another
tool manages the shared CRDs, use that tool's upgrade workflow instead.

The standalone `charts/cloudflared-gateway/charts/gateway-api-crds` chart can
install the shared bundle independently of the controller. Like the parent
chart, it does not upgrade CRDs: apply its `crds/` first, then upgrade its Helm
release to update release-owned safe-upgrade policies. Do not force field
ownership conflicts or remove an existing safe-upgrade policy to bypass a
channel/version rejection; coordinate that change with the cluster administrator.

For the Gateway API v1.6 to v1.7 migration, update existing XBackend objects to
use `spec.port.number` instead of `spec.port.port`, and omit `backendRef.port`
from routes targeting them. There is no automatic field conversion. Use this order:

1. Save the existing XBackend and route manifests, and prepare the updated manifests.
2. Stop **all old controller replicas** and wait for them to exit. Suspend any
   automation that can restart them. Leave the cloudflared deployments running
   with their existing tunnel configuration.
3. Apply matching GA v1.7.x CRDs through their existing owner and the safe-upgrade
   workflow above, then apply the updated XBackend and route manifests.
4. Upgrade the controller release and start the new controller only after the
   objects are migrated. Do not use a rolling controller update across this
   schema change: an old controller can read `spec.port.number` as zero and push
   an invalid origin port.

An unmigrated XBackend reports `Accepted=False` and its routes report
`ResolvedRefs=False`, both with reason `InvalidBackendPort`, instead of routing
to port zero. The XBackend CRD needs Kubernetes 1.32+ for its CEL validation.
`tls.validation` remains required by the CRD for `tls.mode: ServerOnly`, even
though validation is optional in the Go API type.

External hostname backends remain supported. `EndpointSelector` and session
persistence remain unsupported and are rejected. Supported XBackend protocols
are HTTP, HTTP11, HTTP2/GRPC with ServerOnly TLS, and plain TCP. H2C, MCP, mutual
TLS, and WSS (introduced in v1.7) are unsupported and rejected.

The chart does not fetch CRDs over the network. Plain `helm upgrade` does not
update the Gateway API CRDs. The controller's startup checks report missing or
incompatible APIs.

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
