#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
chart="$root/charts/cloudflared-gateway"
hook=templates/gatewayapi-crd-install-hook.yaml
render() {
  helm template review "$chart" --set cloudflare.existingSecret=test-credentials "$@"
}
render_named() {
  local release="$1"
  shift
  helm template "$release" "$chart" --set cloudflare.existingSecret=test-credentials "$@"
}
fail() { echo "$*" >&2; exit 1; }
require_contains() {
  [[ "$1" == *"$2"* ]] || fail "Missing expected output: $2"
}
require_absent() {
  [[ "$1" != *"$2"* ]] || fail "Unexpected output: $2"
}
require_failure() {
  local expected="$1" output
  shift
  if output=$(render "$@" 2>&1); then
    fail "Expected rendering to fail: $*"
  fi
  require_contains "$output" "$expected"
}
# Print the YAML document of the given kind from a multi-document render.
doc() {
  awk -v kind="kind: $2" '/^---/{if (hit) exit; buf=""; next} {buf=buf $0 "\n"} $0==kind{hit=1} END{if (hit) printf "%s", buf}' <<<"$1"
}
# Print the pod template label block of a workload document.
pod_labels() {
  awk '/^  template:/{t=1} t&&/^      labels:/{l=1; next} l&&!/^        /{exit} l{sub(/^ +/, ""); print}' <<<"$1"
}

install=(--kube-version 1.30.0 --set experimental.installGatewayAPICRDs=true)
stable_error='must be a GA v1 release >=v1.6.0'
xbackend_error='must be a GA v1.6.x release'

# Default render: no hook, no experimental env.
output=$(render --kube-version 1.28.0)
require_absent "$output" 'kind: Job'
require_absent "$output" 'ENABLE_EXPERIMENTAL_BACKENDS'

# Pinned hook version matches the Gateway API module the controller is built against.
gomod_version=$(awk '$1 == "sigs.k8s.io/gateway-api" {print $2}' "$root/go.mod")
[[ "$gomod_version" =~ ^v1\.[0-9]+\.[0-9]+$ ]] || fail "Could not read gateway-api version from go.mod: $gomod_version"
values_version=$(awk '$1 == "gatewayAPIVersion:" {print $2}' "$chart/values.yaml")
[[ "$values_version" == "$gomod_version" ]] || fail "values.yaml gatewayAPIVersion $values_version != go.mod $gomod_version"

output=$(render "${install[@]}")
require_contains "$output" '"--field-manager=review-cloudflared-gateway-crd-install"'
require_contains "$output" "\"https://github.com/kubernetes-sigs/gateway-api/releases/download/$gomod_version/standard-install.yaml\""
require_absent "$output" '--force-conflicts'
require_absent "$output" '_xbackends.yaml'
require_absent "$output" 'ENABLE_EXPERIMENTAL_BACKENDS'

output=$(render "${install[@]}" --set experimental.backends.enabled=true --set experimental.gatewayAPIVersion=v1.6.2)
require_contains "$output" '"https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.6.2/standard-install.yaml"'
require_contains "$output" '"https://raw.githubusercontent.com/kubernetes-sigs/gateway-api/v1.6.2/config/crd/experimental/gateway.networking.x-k8s.io_xbackends.yaml"'
require_contains "$output" 'ENABLE_EXPERIMENTAL_BACKENDS'
output=$(render --kube-version 1.35.0 --set experimental.installGatewayAPICRDs=true --set experimental.crdInstaller.forceConflicts=true)
require_contains "$output" '--force-conflicts'

for version in 1.28.0 1.29.9; do
  require_failure 'requires Kubernetes >=1.30' --kube-version "$version" --set experimental.installGatewayAPICRDs=true
done

# Standard-only install (XBackend off) still requires a stable v1 bundle >=v1.6.0.
for version in v1.5.0 v1.5.1 v1.0.0 v0.8.0 v2.0.0 v1.6.0-rc.1 v1.7.0-rc.1 v1.6.3+build v1.6.03 v1.06.0 1.6.3 v1.6 v1 latest main invalid ' v1.6.3' ''; do
  require_failure "$stable_error" "${install[@]}" --set-string experimental.gatewayAPIVersion="$version"
done
# Compatible newer stable releases are accepted (v1.10.0 catches lexical comparison).
for version in v1.6.0 v1.6.3 v1.7.0 v1.10.0; do
  output=$(render "${install[@]}" --set-string experimental.gatewayAPIVersion="$version")
  require_contains "$output" "\"https://github.com/kubernetes-sigs/gateway-api/releases/download/$version/standard-install.yaml\""
  require_absent "$output" '_xbackends.yaml'
done

# XBackend keeps the narrower v1.6.x line.
for version in v1.5.1 v1.7.0 v1.10.0 v1.6.0-rc.1 v1.6.03 invalid; do
  if output=$(render "${install[@]}" --set experimental.backends.enabled=true --set-string experimental.gatewayAPIVersion="$version" 2>&1); then
    fail "Expected XBackend rendering to fail for $version"
  fi
  [[ "$output" == *"$xbackend_error"* || "$output" == *"$stable_error"* ]] || fail "Unexpected error for $version: $output"
done
require_failure "$xbackend_error" "${install[@]}" --set experimental.backends.enabled=true --set-string experimental.gatewayAPIVersion=v1.7.0

# Disabled installer never validates installer-only values.
for version in v1.5.1 v1.7.0 invalid ''; do
  for backends in true false; do
    output=$(render --kube-version 1.28.0 --set experimental.backends.enabled="$backends" --set-string experimental.gatewayAPIVersion="$version")
    require_absent "$output" 'kind: Job'
    require_absent "$output" 'crd-install'
  done
done

# Long names stay within 63 characters, keep the suffix, and never produce "--".
long_override="$(printf 'a%.0s' {1..50})-$(printf 'b%.0s' {1..12})"
for args in "my-company-platform-ingress-prod" \
            "review --set fullnameOverride=$long_override" \
            "review --set nameOverride=$(printf 'n%.0s' {1..63})"; do
  read -r -a parts <<<"$args"
  output=$(render_named "${parts[0]}" "${parts[@]:1}" "${install[@]}" --show-only "$hook")
  names=$(awk '/^  name: /{print $2} /^    app.kubernetes.io\/name: /{print $2} /^        app.kubernetes.io\/name: /{print $2}' <<<"$output" | sort -u)
  [[ -n "$names" ]] || fail "No hook names rendered for $args"
  while read -r name; do
    (( ${#name} <= 63 )) || fail "Hook name exceeds 63 characters (${#name}): $name"
    [[ "$name" == *-crd-install ]] || fail "Hook name lost its suffix: $name"
    [[ "$name" != *--* ]] || fail "Hook name contains a double hyphen: $name"
  done <<<"$names"
  manager=$(grep -o -- '--field-manager=[^"]*' <<<"$output")
  job=$(awk '/^  name: /{n=$2} /^kind: Job/{j=1} j&&/^  name: /{print $2; exit}' <<<"$output")
  [[ "$manager" == "--field-manager=$job" ]] || fail "Field manager $manager does not match Job $job"
done
output=$(render_named my-company-platform-ingress-prod "${install[@]}" --show-only "$hook")
require_contains "$output" 'name: my-company-platform-ingress-prod-cloudflared-gatewa-crd-install'

# Hook pods must not match controller selectors (NetworkPolicy/PDB) but keep the instance label.
output=$(render "${install[@]}")
controller_labels=$(pod_labels "$(doc "$output" Deployment)")
hook_labels=$(pod_labels "$(doc "$output" Job)")
require_contains "$controller_labels" 'app.kubernetes.io/name: cloudflared-gateway'
require_contains "$hook_labels" 'app.kubernetes.io/name: cloudflared-gateway-crd-install'
require_contains "$hook_labels" 'app.kubernetes.io/instance: review'
while read -r label; do
  [[ "$label" == app.kubernetes.io/instance:* ]] && continue
  if grep -qxF -- "$label" <<<"$hook_labels"; then
    selector_overlap=$((${selector_overlap:-0} + 1))
  fi
done < <(awk '/^    matchLabels:/{m=1; next} m&&!/^      /{exit} m{sub(/^ +/, ""); print}' <<<"$(doc "$output" Deployment)")
(( ${selector_overlap:-0} == 0 )) || fail "Hook pod labels satisfy the controller selector"

# Controller pod constraints also apply to the installer pod.
output=$(render "${install[@]}" --show-only "$hook" \
  --set 'imagePullSecrets[0].name=regcred' \
  --set nodeSelector.pool=infra \
  --set 'tolerations[0].key=dedicated' --set 'tolerations[0].operator=Exists' --set 'tolerations[0].effect=NoSchedule' \
  --set resources.limits.memory=99Mi --set resources.requests.cpu=7m \
  --set 'affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key=zone' \
  --set 'affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator=Exists' \
  --set 'affinity.podAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].topologyKey=kubernetes.io/hostname' \
  --set 'affinity.podAffinity.requiredDuringSchedulingIgnoredDuringExecution[0].labelSelector.matchLabels.app=controller-only')
job=$(doc "$output" Job)
for expected in 'imagePullSecrets:' '- name: regcred' 'nodeSelector:' 'pool: infra' 'key: dedicated' 'effect: NoSchedule' \
                'memory: 99Mi' 'cpu: 7m' 'nodeAffinity:' 'key: zone'; do
  require_contains "$job" "$expected"
done
# Controller pod affinity could make the installer unschedulable before controller pods exist.
require_absent "$job" 'podAffinity'
require_absent "$job" 'controller-only'
output=$(render "${install[@]}" --show-only "$hook" --set resources=null)
require_absent "$(doc "$output" Job)" 'resources:'
require_absent "$output" 'affinity:'
require_absent "$output" 'nodeSelector:'
require_absent "$output" 'imagePullSecrets:'

echo 'Chart installation safety checks passed.'
