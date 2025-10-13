#!/bin/bash

# Update KIND Image Script
# This script builds the insights-ros-ingress container image from current workspace code,
# loads it into the KIND cluster, and patches the deployment to use the new image.
# Use this for local development testing before pushing to registry.

set -e  # Exit on any error

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
KIND_CLUSTER_NAME=${KIND_CLUSTER_NAME:-ros-ocp-cluster}
NAMESPACE=${NAMESPACE:-ros-ocp}
HELM_RELEASE_NAME=${HELM_RELEASE_NAME:-ros-ocp}
CONTAINER_RUNTIME=${CONTAINER_RUNTIME:-podman}
IMAGE_TAG=${IMAGE_TAG:-dev-$(date +%s)}
REGISTRY=${REGISTRY:-quay.io/insights-onprem}
APP_NAME=${APP_NAME:-insights-ros-ingress}
FULL_IMAGE="${REGISTRY}/${APP_NAME}:${IMAGE_TAG}"

echo_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

echo_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

echo_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

echo_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to check if command exists
command_exists() {
    command -v "$1" >/dev/null 2>&1
}

# Function to check prerequisites
check_prerequisites() {
    echo_info "Checking prerequisites..."

    local missing_tools=()

    if ! command_exists kubectl; then
        missing_tools+=("kubectl")
    fi

    if ! command_exists kind; then
        missing_tools+=("kind")
    fi

    if ! command_exists "$CONTAINER_RUNTIME"; then
        missing_tools+=("$CONTAINER_RUNTIME")
    fi

    if [ ${#missing_tools[@]} -gt 0 ]; then
        echo_error "Missing required tools: ${missing_tools[*]}"
        return 1
    fi

    # Check if KIND cluster exists
    if ! kind get clusters | grep -q "^${KIND_CLUSTER_NAME}$"; then
        echo_error "KIND cluster '${KIND_CLUSTER_NAME}' not found"
        echo_info "Available clusters:"
        kind get clusters || echo "  No clusters found"
        echo_info "Create cluster with: make deploy-kind"
        return 1
    fi

    # Check if namespace exists
    if ! kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; then
        echo_error "Namespace '${NAMESPACE}' not found"
        echo_info "Deploy Helm chart first with: make helm-install"
        return 1
    fi

    echo_success "All prerequisites met"
    return 0
}

# Function to build container image
build_image() {
    echo_info "Building container image from current workspace..."
    echo_info "Image: ${FULL_IMAGE}"

    cd "$PROJECT_ROOT"

    # Build using Makefile
    if ! IMAGE_TAG="$IMAGE_TAG" make build-image; then
        echo_error "Failed to build container image"
        return 1
    fi

    # Verify image was built
    echo_info "Verifying built image..."
    if ! $CONTAINER_RUNTIME images "$FULL_IMAGE" --format "{{.Repository}}:{{.Tag}}" | grep -q "$FULL_IMAGE"; then
        echo_error "Image not found after build: ${FULL_IMAGE}"
        echo_info "Available images:"
        $CONTAINER_RUNTIME images | grep -E "(REPOSITORY|${APP_NAME})" || true
        return 1
    fi

    echo_success "Container image built successfully"
    return 0
}

# Function to load image into KIND cluster
load_image_to_kind() {
    echo_info "Loading image into KIND cluster '${KIND_CLUSTER_NAME}'..."

    # Create temporary archive
    local temp_archive="/tmp/${APP_NAME}-${IMAGE_TAG}.tar"

    echo_info "Saving image to archive..."
    if ! $CONTAINER_RUNTIME save "$FULL_IMAGE" -o "$temp_archive"; then
        echo_error "Failed to save image to archive"
        return 1
    fi

    # Verify archive was created
    if [ ! -f "$temp_archive" ]; then
        echo_error "Archive not found: $temp_archive"
        return 1
    fi

    local archive_size=$(du -h "$temp_archive" | cut -f1)
    echo_info "Archive size: ${archive_size}"

    # Configure KIND to use the same container runtime
    if [ "$CONTAINER_RUNTIME" = "podman" ]; then
        export KIND_EXPERIMENTAL_PROVIDER=podman
        echo_info "Configured KIND to use Podman runtime"
    fi

    # Load into KIND cluster
    echo_info "Loading archive into KIND cluster..."
    if ! kind load image-archive "$temp_archive" --name "$KIND_CLUSTER_NAME"; then
        echo_error "Failed to load image into KIND cluster"
        rm -f "$temp_archive"
        return 1
    fi

    # Cleanup archive
    rm -f "$temp_archive"

    echo_success "Image loaded into KIND cluster"
    return 0
}

# Function to patch deployment
patch_deployment() {
    echo_info "Patching deployment to use new image..."

    # Find the ingress deployment - try multiple label selectors
    local deployment_name=$(kubectl get deployment -n "$NAMESPACE" -l "app.kubernetes.io/name=insights-ros-ingress,app.kubernetes.io/instance=${HELM_RELEASE_NAME}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)

    # If not found, try with the component label
    if [ -z "$deployment_name" ]; then
        deployment_name=$(kubectl get deployment -n "$NAMESPACE" -l "app.kubernetes.io/component=ingress,app.kubernetes.io/instance=${HELM_RELEASE_NAME}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
    fi

    # If still not found, try matching by name pattern
    if [ -z "$deployment_name" ]; then
        deployment_name=$(kubectl get deployment -n "$NAMESPACE" -o jsonpath='{.items[?(@.metadata.name contains "ingress")].metadata.name}' 2>/dev/null | awk '{print $1}')
    fi

    if [ -z "$deployment_name" ]; then
        echo_error "Could not find insights-ros-ingress deployment in namespace '${NAMESPACE}'"
        echo_info "Available deployments:"
        kubectl get deployments -n "$NAMESPACE" || true
        return 1
    fi

    echo_info "Found deployment: ${deployment_name}"

    # Get the container name from the deployment
    local container_name=$(kubectl get deployment "$deployment_name" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].name}' 2>/dev/null)
    if [ -z "$container_name" ]; then
        echo_error "Could not determine container name in deployment"
        return 1
    fi
    echo_info "Container name: ${container_name}"

    # Patch the deployment with new image and pullPolicy
    echo_info "Updating image to: ${FULL_IMAGE}"
    if ! kubectl set image deployment/"$deployment_name" "${container_name}=$FULL_IMAGE" -n "$NAMESPACE"; then
        echo_error "Failed to update deployment image"
        return 1
    fi

    # Set pull policy to Never (or IfNotPresent for local images)
    echo_info "Setting image pull policy to Never for local image..."
    kubectl patch deployment "$deployment_name" -n "$NAMESPACE" --type='json' \
        -p='[{"op": "replace", "path": "/spec/template/spec/containers/0/imagePullPolicy", "value":"Never"}]' 2>/dev/null || \
    kubectl patch deployment "$deployment_name" -n "$NAMESPACE" --type='json' \
        -p='[{"op": "add", "path": "/spec/template/spec/containers/0/imagePullPolicy", "value":"Never"}]'

    echo_success "Deployment patched successfully"

    # Wait for rollout
    echo_info "Waiting for deployment rollout..."
    if kubectl rollout status deployment/"$deployment_name" -n "$NAMESPACE" --timeout=120s; then
        echo_success "Deployment rollout completed"
    else
        echo_error "Deployment rollout failed or timed out"
        echo_info "Check pod status with: kubectl get pods -n $NAMESPACE"
        return 1
    fi

    return 0
}

# Function to show deployment status
show_status() {
    echo_info "Current deployment status:"
    echo ""

    echo_info "Pods:"
    kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/name=insights-ros-ingress" -o wide
    echo ""

    echo_info "Deployment image:"
    kubectl get deployment -n "$NAMESPACE" -l "app.kubernetes.io/name=insights-ros-ingress" \
        -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.template.spec.containers[0].image}{" (pullPolicy: "}{.spec.template.spec.containers[0].imagePullPolicy}{")"}{"\n"}{end}'
    echo ""

    echo_info "Recent events:"
    kubectl get events -n "$NAMESPACE" --sort-by='.lastTimestamp' --field-selector involvedObject.kind=Pod | tail -10
}

# Function to show help
show_help() {
    cat <<EOF
Update KIND Image Script
========================

This script builds the insights-ros-ingress container image from the current
workspace code, loads it into your KIND cluster, and patches the deployment
to use the new image for testing.

Usage: $0 [options]

Options:
  -h, --help           Show this help message
  -n, --namespace      Kubernetes namespace (default: ros-ocp)
  -c, --cluster        KIND cluster name (default: ros-ocp-cluster)
  -r, --runtime        Container runtime: podman or docker (default: podman)
  -t, --tag            Image tag (default: dev-<timestamp>)
  --skip-build         Skip building the image (use existing)
  --skip-load          Skip loading to KIND (assume already loaded)
  --status-only        Only show current deployment status

Environment Variables:
  KIND_CLUSTER_NAME    KIND cluster name
  NAMESPACE            Kubernetes namespace
  HELM_RELEASE_NAME    Helm release name
  CONTAINER_RUNTIME    Container runtime (podman or docker)
  IMAGE_TAG            Image tag to use
  REGISTRY             Container registry
  APP_NAME             Application name

Example:
  # Build, load, and patch with default settings
  $0

  # Use custom tag and namespace
  IMAGE_TAG=my-test NAMESPACE=my-namespace $0

  # Only show current status
  $0 --status-only

  # Skip building (use existing image)
  $0 --skip-build

Prerequisites:
  1. KIND cluster must be running: make deploy-kind
  2. Helm chart must be deployed: make helm-install
  3. Container runtime (podman/docker) must be installed

EOF
}

# Main execution
main() {
    local skip_build=false
    local skip_load=false
    local status_only=false

    # Parse arguments
    while [ $# -gt 0 ]; do
        case "$1" in
            -h|--help)
                show_help
                exit 0
                ;;
            -n|--namespace)
                NAMESPACE="$2"
                shift 2
                ;;
            -c|--cluster)
                KIND_CLUSTER_NAME="$2"
                shift 2
                ;;
            -r|--runtime)
                CONTAINER_RUNTIME="$2"
                shift 2
                ;;
            -t|--tag)
                IMAGE_TAG="$2"
                FULL_IMAGE="${REGISTRY}/${APP_NAME}:${IMAGE_TAG}"
                shift 2
                ;;
            --skip-build)
                skip_build=true
                shift
                ;;
            --skip-load)
                skip_load=true
                shift
                ;;
            --status-only)
                status_only=true
                shift
                ;;
            *)
                echo_error "Unknown option: $1"
                echo_info "Use --help for usage information"
                exit 1
                ;;
        esac
    done

    echo_info "Update KIND Image for Local Testing"
    echo_info "====================================="
    echo_info "Cluster: ${KIND_CLUSTER_NAME}"
    echo_info "Namespace: ${NAMESPACE}"
    echo_info "Image: ${FULL_IMAGE}"
    echo_info "Runtime: ${CONTAINER_RUNTIME}"
    echo ""

    # Check prerequisites
    if ! check_prerequisites; then
        exit 1
    fi

    # Status only mode
    if [ "$status_only" = true ]; then
        show_status
        exit 0
    fi

    # Build image
    if [ "$skip_build" = false ]; then
        if ! build_image; then
            exit 1
        fi
    else
        echo_warning "Skipping build (--skip-build specified)"
    fi

    # Load image to KIND
    if [ "$skip_load" = false ]; then
        if ! load_image_to_kind; then
            exit 1
        fi
    else
        echo_warning "Skipping load (--skip-load specified)"
    fi

    # Patch deployment
    if ! patch_deployment; then
        echo_error "Failed to patch deployment"
        exit 1
    fi

    echo ""
    echo_success "Image update completed successfully!"
    echo ""
    show_status
    echo ""
    echo_info "Next steps:"
    echo_info "  - Test your changes"
    echo_info "  - View logs: kubectl logs -n $NAMESPACE -l app.kubernetes.io/name=insights-ros-ingress -f"
    echo_info "  - Run dataflow test: ./deployments/kubernetes/scripts/test-k8s-dataflow.sh"
    echo_info "  - Revert to registry image: make helm-install"
}

# Run main function
main "$@"

