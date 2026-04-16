#!/usr/bin/env bash
# Packages the Helm chart into dist/ with the given version.
# Called from .goreleaser.yaml before.hooks; the resulting .tgz is attached
# to the GitHub Release via release.extra_files.
set -euo pipefail

VERSION="${1:?version required (without leading v)}"
CHART_DIR="charts/cloudflared-gateway"

mkdir -p dist

helm package "${CHART_DIR}" \
  --version "${VERSION}" \
  --app-version "${VERSION}" \
  --destination dist/

echo "Packaged: dist/cloudflared-gateway-${VERSION}.tgz"
