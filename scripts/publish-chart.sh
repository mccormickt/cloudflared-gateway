#!/usr/bin/env bash
# Pushes the packaged Helm chart to GHCR as an OCI artifact and signs it
# with cosign (keyless via GitHub OIDC in CI).
# Called from .goreleaser.yaml after.hooks.
set -euo pipefail

VERSION="${1:?version required (without leading v)}"
OCI_PARENT="${CHART_OCI_REPO:-oci://ghcr.io/mccormickt/charts}"
CHART_REPO="${OCI_PARENT#oci://}/cloudflared-gateway"
TGZ="dist/cloudflared-gateway-${VERSION}.tgz"

[[ -f "${TGZ}" ]] || { echo "Chart tarball not found: ${TGZ}" >&2; exit 1; }

PUSH_OUT="$(helm push "${TGZ}" "${OCI_PARENT}" 2>&1)"
echo "${PUSH_OUT}"

DIGEST="$(awk '/^Digest:/ {print $2}' <<<"${PUSH_OUT}")"
[[ -n "${DIGEST}" ]] || { echo "Failed to extract digest from helm push output" >&2; exit 1; }

cosign sign --yes "${CHART_REPO}@${DIGEST}"

echo "Chart published and signed: ${CHART_REPO}:${VERSION} @${DIGEST}"
