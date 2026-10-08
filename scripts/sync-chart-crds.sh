#!/usr/bin/env bash
# Mirrors the generated CRDs in config/crd/ into the Helm chart's crds/ directory
# using the chart's singular file naming (e.g.
# cloudflare.jan0ski.net_cloudflareaccesspolicies.yaml -> cloudflareaccesspolicy.yaml).
#
# Run after `make manifests`. CI runs this followed by `git diff --exit-code` to
# fail when the chart CRDs have drifted from config/crd/.
set -euo pipefail

SRC_DIR="config/crd"
DST_DIR="charts/cloudflared-gateway/crds"

mkdir -p "${DST_DIR}"
# Start clean so a CRD removed from config/crd/ doesn't leave a stale chart copy.
rm -f "${DST_DIR}"/*.yaml

# singularize turns a controller-gen plural resource name into the chart's
# singular file name: "...policies" -> "...policy", "...configs" -> "...config".
singularize() {
  local n="$1"
  case "$n" in
    *ies) printf '%sy' "${n%ies}" ;;
    *s)   printf '%s'  "${n%s}" ;;
    *)    printf '%s'  "$n" ;;
  esac
}

shopt -s nullglob
for src in "${SRC_DIR}"/*.yaml; do
  base="$(basename "${src}" .yaml)" # <group>_<plural>
  resource="${base#*_}"             # <plural>
  name="$(singularize "${resource}")"
  cp "${src}" "${DST_DIR}/${name}.yaml"
  echo "synced ${src} -> ${DST_DIR}/${name}.yaml"
done
