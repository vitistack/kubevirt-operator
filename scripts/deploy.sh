#!/bin/bash
# Deployment helper script

set -e

DEPLOYMENT_MODE=${1:-"standalone"}

case $DEPLOYMENT_MODE in
    "standalone")
        echo "🚀 Deploying with embedded CRDs (standalone mode)"
        kubectl apply -k config/default
        ;;
    "external-crds")
        echo "🚀 Deploying without CRDs (assuming external CRD management)"
        echo "⚠️  Make sure CRDs are installed from github.com/vitistack/crds first!"
        kubectl apply -k config/default -f config/default/kustomization-without-crds.yaml
        ;;
    *)
        echo "❌ Unknown deployment mode: $DEPLOYMENT_MODE"
        echo "Usage: $0 [standalone|external-crds]"
        echo "  standalone    - Deploy with embedded CRDs (default)"
        echo "  external-crds - Deploy without CRDs (assumes external management)"
        exit 1
        ;;
esac

echo "✅ Deployment completed in $DEPLOYMENT_MODE mode"
