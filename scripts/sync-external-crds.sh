#!/bin/bash
# Script to sync CRD manifests from external repo

set -e

EXTERNAL_REPO="github.com/vitistack/crds"
EXTERNAL_VERSION="v0.0.1-alpha04"
CRD_SOURCE_URL="https://raw.githubusercontent.com/vitistack/crds/${EXTERNAL_VERSION}/crds"

echo "🔄 Syncing CRD manifests from ${EXTERNAL_REPO}@${EXTERNAL_VERSION}"

# Create manifests directory if it doesn't exist
mkdir -p config/external-crds

# Download Machine CRD manifest
echo "📥 Downloading Machine CRD manifest..."
curl -fsSL "${CRD_SOURCE_URL}/vitistack.io_machines.yaml" -o config/external-crds/vitistack.io_machines.yaml || {
    echo "⚠️  Failed to download from URL, checking if manifests exist in the repo..."
    # Fallback: try to find manifests in the go mod cache
    GOMOD_PATH=$(go env GOMODCACHE)/github.com/vitistack/crds@${EXTERNAL_VERSION}
    if [ -d "${GOMOD_PATH}" ]; then
        echo "📂 Found external repo in go mod cache"
        find "${GOMOD_PATH}" -name "vitistack.io_machines.yaml" -exec cp {} config/external-crds/ \;
    else
        echo "❌ Could not find external CRD manifests"
        exit 1
    fi
}

# Create kustomization.yaml for external CRDs
cat > config/external-crds/kustomization.yaml << EOF
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - vitistack.io_machines.yaml
EOF

echo "✅ CRD manifests synced successfully!"
echo "📁 Files available in config/external-crds/"
ls -la config/external-crds/
