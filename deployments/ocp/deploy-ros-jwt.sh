#!/bin/bash
set -euo pipefail

################################################################################
# ROS OpenShift JWT Authentication Deployment Script
#
# This script orchestrates the complete JWT authentication setup for ROS on
# OpenShift by wrapping the authoritative scripts from ros-helm-chart repository.
#
# Based on: https://github.com/insights-onprem/ros-helm-chart/blob/main/scripts/README.md
# Section: JWT Authentication Setup
#
# Usage:
#   ./deploy-ros-jwt.sh [OPTIONS]
#
# Options:
#   --skip-rhsso              Skip RHSSO/Keycloak deployment
#   --skip-strimzi            Skip Kafka/Strimzi deployment
#   --skip-authorino          Skip Authorino OAuth2 deployment
#   --skip-helm               Skip ROS Helm chart installation
#   --skip-tls                Skip TLS certificate setup
#   --skip-test               Skip JWT authentication test
#   --namespace NAME          Target namespace (default: ros-ocp)
#   --image-tag TAG           Custom image tag for insights-ros-ingress
#   --use-local-chart         Use local Helm chart instead of GitHub release
#   --verbose                 Enable verbose output
#   --dry-run                 Show what would be executed without running
#   --help                    Display this help message
#
# Environment Variables:
#   KUBECONFIG               Path to kubeconfig file (default: ~/.kube/config)
#   KUBEADMIN_PASSWORD_FILE  Path to kubeadmin password file
#   SHARED_DIR               Shared directory containing kubeadmin-password
#   OPENSHIFT_API            OpenShift API URL (auto-detected from kubeconfig)
#   OPENSHIFT_USERNAME       OpenShift username (default: kubeadmin)
#   OPENSHIFT_PASSWORD       OpenShift password (auto-detected from files)
#   QUAY_USERNAME            Quay.io username for pulling images
#   QUAY_PASSWORD            Quay.io password for pulling images
#   IMAGE_REGISTRY           Image registry (default: quay.io)
#   IMAGE_REPOSITORY         Image repository (default: insights-onprem/insights-ros-ingress)
#
# Note: This script will automatically login to OpenShift using credentials from:
#       1. KUBECONFIG file (for API URL)
#       2. KUBEADMIN_PASSWORD_FILE or SHARED_DIR/kubeadmin-password (for password)
#       If already logged in, it will skip the login step.
#
# Prerequisites:
#   - oc CLI installed and configured
#   - helm CLI installed (v3+)
#   - yq installed for YAML/JSON processing
#   - curl installed for downloading scripts
#   - OpenShift cluster with admin access
#
# Example:
#   # Full deployment with custom image
#   ./deploy-ros-jwt.sh --image-tag main-abc123
#
#   # Skip RHSSO if already deployed
#   ./deploy-ros-jwt.sh --skip-rhsso --namespace ros-production
#
#   # Dry run to preview actions
#   ./deploy-ros-jwt.sh --dry-run --verbose
#
################################################################################

# Script metadata
SCRIPT_VERSION="1.0.4"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Default configuration
NAMESPACE="${NAMESPACE:-ros-ocp}"
IMAGE_REGISTRY="${IMAGE_REGISTRY:-quay.io}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-insights-onprem/insights-ros-ingress}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
USE_LOCAL_CHART="${USE_LOCAL_CHART:-false}"
VERBOSE="${VERBOSE:-false}"
DRY_RUN="${DRY_RUN:-false}"

# OpenShift authentication
KUBECONFIG="${KUBECONFIG:-${HOME}/.kube/config}"
OPENSHIFT_USERNAME="${OPENSHIFT_USERNAME:-kubeadmin}"
OPENSHIFT_API="${OPENSHIFT_API:-}"
OPENSHIFT_PASSWORD="${OPENSHIFT_PASSWORD:-}"
KUBEADMIN_PASSWORD_FILE="${KUBEADMIN_PASSWORD_FILE:-}"
SHARED_DIR="${SHARED_DIR:-}"

# Script URLs from ros-helm-chart repository
ROS_HELM_CHART_SCRIPTS_URL="https://raw.githubusercontent.com/insights-onprem/ros-helm-chart/main/scripts"
SCRIPT_DEPLOY_RHSSO="deploy-rhsso.sh"
SCRIPT_DEPLOY_STRIMZI="deploy-strimzi.sh"
SCRIPT_INSTALL_AUTHORINO="install-authorino.sh"
SCRIPT_INSTALL_HELM="install-helm-chart.sh"
SCRIPT_SETUP_TLS="setup-cost-mgmt-tls.sh"
SCRIPT_TEST_JWT="test-ocp-dataflow-jwt.sh"

# Step flags (default: run all steps)
SKIP_RHSSO=false
SKIP_STRIMZI=false
SKIP_AUTHORINO=false
SKIP_HELM=false
SKIP_TLS=false
SKIP_TEST=false

# Temporary directory for downloaded scripts
TEMP_DIR=$(mktemp -d)
trap 'rm -rf "${TEMP_DIR}"' EXIT

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

################################################################################
# Logging functions
################################################################################

log_info() {
    echo -e "${BLUE}ℹ INFO:${NC} $*"
}

log_success() {
    echo -e "${GREEN}✅ SUCCESS:${NC} $*"
}

log_warning() {
    echo -e "${YELLOW}⚠ WARNING:${NC} $*"
}

log_error() {
    echo -e "${RED}❌ ERROR:${NC} $*" >&2
}

log_step() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${CYAN}▶ STEP: $*${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
}

log_verbose() {
    if [[ "${VERBOSE}" == "true" ]]; then
        echo -e "${CYAN}[VERBOSE]${NC} $*"
    fi
}

################################################################################
# Utility functions
################################################################################

show_help() {
    sed -n '/^# Usage:/,/^################################################################################$/p' "$0" | sed 's/^# \?//'
    exit 0
}

check_prerequisites() {
    log_step "Checking prerequisites"
    
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_info "DRY RUN: Skipping prerequisite checks"
        return 0
    fi
    
    local missing_tools=()
    
    # Check required tools
    for tool in oc helm yq curl; do
        if ! command -v "$tool" &> /dev/null; then
            missing_tools+=("$tool")
        else
            log_verbose "Found: $tool ($(command -v "$tool"))"
        fi
    done
    
    if [[ ${#missing_tools[@]} -gt 0 ]]; then
        log_error "Missing required tools: ${missing_tools[*]}"
        log_error "Please install missing tools and try again"
        log_error ""
        log_error "Installation instructions:"
        log_error "  macOS:  brew install yq"
        log_error "  Linux:  See https://github.com/mikefarah/yq#install"
        exit 1
    fi
    
    log_success "All required tools are installed"
}

detect_openshift_credentials() {
    log_verbose "Detecting OpenShift credentials from environment..."
    
    # Detect API URL from kubeconfig if not set
    if [[ -z "${OPENSHIFT_API}" ]] && [[ -f "${KUBECONFIG}" ]]; then
        OPENSHIFT_API=$(yq e '.clusters[0].cluster.server' "${KUBECONFIG}" 2>/dev/null || echo "")
        if [[ -n "${OPENSHIFT_API}" ]]; then
            log_verbose "Detected API URL from kubeconfig: ${OPENSHIFT_API}"
        fi
    fi
    
    # Detect password from files if not set
    if [[ -z "${OPENSHIFT_PASSWORD}" ]]; then
        if [[ -n "${KUBEADMIN_PASSWORD_FILE}" ]] && [[ -s "${KUBEADMIN_PASSWORD_FILE}" ]]; then
            OPENSHIFT_PASSWORD="$(cat "${KUBEADMIN_PASSWORD_FILE}")"
            log_verbose "Loaded password from KUBEADMIN_PASSWORD_FILE"
        elif [[ -n "${SHARED_DIR}" ]] && [[ -s "${SHARED_DIR}/kubeadmin-password" ]]; then
            OPENSHIFT_PASSWORD="$(cat "${SHARED_DIR}/kubeadmin-password")"
            log_verbose "Loaded password from SHARED_DIR/kubeadmin-password"
        fi
    fi
}

login_to_openshift() {
    log_step "Logging into OpenShift"
    
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_info "DRY RUN: Would login to OpenShift"
        return 0
    fi
    
    # Detect credentials from environment
    detect_openshift_credentials
    
    # Check if credentials are available
    if [[ -z "${OPENSHIFT_API}" ]]; then
        log_error "OPENSHIFT_API not set and could not be detected from kubeconfig"
        log_error "Please set OPENSHIFT_API environment variable or ensure KUBECONFIG is valid"
        return 1
    fi
    
    if [[ -z "${OPENSHIFT_PASSWORD}" ]]; then
        log_error "OPENSHIFT_PASSWORD not set and could not be detected from files"
        log_error "Please set one of:"
        log_error "  - OPENSHIFT_PASSWORD environment variable"
        log_error "  - KUBEADMIN_PASSWORD_FILE pointing to password file"
        log_error "  - SHARED_DIR containing kubeadmin-password file"
        return 1
    fi
    
    # Configure kubeconfig to skip TLS verification
    if [[ -f "${KUBECONFIG}" ]]; then
        log_verbose "Configuring kubeconfig to skip TLS verification..."
        yq -i 'del(.clusters[].cluster.certificate-authority-data) | .clusters[].cluster.insecure-skip-tls-verify=true' "${KUBECONFIG}" 2>/dev/null || true
    fi
    
    # Attempt login
    log_info "Logging in as ${OPENSHIFT_USERNAME} to ${OPENSHIFT_API}..."
    if oc login "${OPENSHIFT_API}" \
        --username="${OPENSHIFT_USERNAME}" \
        --password="${OPENSHIFT_PASSWORD}" \
        --insecure-skip-tls-verify=true &> /dev/null; then
        log_success "Successfully logged into OpenShift"
    else
        log_error "Failed to login to OpenShift"
        log_error "Please verify credentials and API URL"
        return 1
    fi
}

check_oc_connection() {
    log_step "Verifying OpenShift connection"
    
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_info "DRY RUN: Would verify OpenShift connection"
        return 0
    fi
    
    # Check if already logged in
    if ! oc whoami &> /dev/null; then
        log_info "Not currently logged into OpenShift, attempting automatic login..."
        
        # Try to login automatically
        if ! login_to_openshift; then
            log_error "Automatic login failed"
            log_error ""
            log_error "Manual login options:"
            log_error "  1. Set environment variables:"
            log_error "     export OPENSHIFT_API='https://api.example.com:6443'"
            log_error "     export OPENSHIFT_PASSWORD='your-password'"
            log_error ""
            log_error "  2. Or login manually:"
            log_error "     oc login https://api.example.com:6443"
            log_error ""
            exit 1
        fi
    else
        log_success "Already logged into OpenShift"
    fi
    
    local current_user
    current_user=$(oc whoami)
    local current_server
    current_server=$(oc whoami --show-server)
    
    log_success "Connected to OpenShift as: ${current_user}"
    log_info "Server: ${current_server}"
    
    # Check if user has admin privileges
    if oc auth can-i create clusterrole &> /dev/null; then
        log_success "User has cluster-admin privileges"
    else
        log_warning "User may not have sufficient privileges for cluster-scoped resources"
        log_warning "Some deployment steps may fail without admin access"
    fi
}

download_script() {
    local script_name="$1"
    local dest_path="${TEMP_DIR}/${script_name}"
    local script_url="${ROS_HELM_CHART_SCRIPTS_URL}/${script_name}"
    
    log_verbose "Downloading: ${script_url}"
    
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_info "DRY RUN: Would download ${script_name}"
        touch "${dest_path}"
        chmod +x "${dest_path}"
        return 0
    fi
    
    if ! curl -fsSL "${script_url}" -o "${dest_path}"; then
        log_error "Failed to download ${script_name}"
        return 1
    fi
    
    chmod +x "${dest_path}"
    log_verbose "Downloaded to: ${dest_path}"
}

execute_script() {
    local script_name="$1"
    shift
    local script_path="${TEMP_DIR}/${script_name}"
    
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_info "DRY RUN: Would execute: ${script_name} $*"
        return 0
    fi
    
    log_info "Executing: ${script_name} $*"
    
    if [[ "${VERBOSE}" == "true" ]]; then
        bash -x "${script_path}" "$@"
    else
        "${script_path}" "$@"
    fi
}

create_namespace() {
    log_step "Creating namespace: ${NAMESPACE}"
    
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_info "DRY RUN: Would create namespace ${NAMESPACE}"
        return 0
    fi
    
    if oc get namespace "${NAMESPACE}" &> /dev/null; then
        log_info "Namespace ${NAMESPACE} already exists"
    else
        oc create namespace "${NAMESPACE}"
        log_success "Created namespace: ${NAMESPACE}"
    fi
    
    # Label namespace for Cost Management Operator
    log_info "Labeling namespace for Cost Management Operator..."
    oc label namespace "${NAMESPACE}" cost_management_optimizations=true --overwrite
    log_success "Namespace labeled successfully"
}

################################################################################
# Deployment steps
################################################################################

deploy_rhsso() {
    if [[ "${SKIP_RHSSO}" == "true" ]]; then
        log_warning "Skipping RHSSO/Keycloak deployment (--skip-rhsso)"
        return 0
    fi
    
    log_step "Step 1/6: Deploying RHSSO/Keycloak"
    
    download_script "${SCRIPT_DEPLOY_RHSSO}"
    
    # Export environment variables for RHSSO script
    export NAMESPACE="${NAMESPACE}"
    
    if [[ "${VERBOSE}" == "true" ]]; then
        export VERBOSE="true"
    fi
    
    execute_script "${SCRIPT_DEPLOY_RHSSO}"
    
    log_success "RHSSO/Keycloak deployment completed"
}

deploy_strimzi() {
    if [[ "${SKIP_STRIMZI}" == "true" ]]; then
        log_warning "Skipping Kafka/Strimzi deployment (--skip-strimzi)"
        return 0
    fi
    
    log_step "Step 2/6: Deploying Kafka/Strimzi"
    
    download_script "${SCRIPT_DEPLOY_STRIMZI}"
    
    # Export environment variables for Strimzi script
    export KAFKA_NAMESPACE="${NAMESPACE}"
    export KAFKA_ENVIRONMENT="ocp"
    
    if [[ "${VERBOSE}" == "true" ]]; then
        export VERBOSE="true"
    fi
    
    execute_script "${SCRIPT_DEPLOY_STRIMZI}"
    
    log_success "Kafka/Strimzi deployment completed"
}

deploy_authorino() {
    if [[ "${SKIP_AUTHORINO}" == "true" ]]; then
        log_warning "Skipping Authorino OAuth2 deployment (--skip-authorino)"
        return 0
    fi
    
    log_step "Step 3/6: Deploying Authorino for OAuth2 authentication"
    
    download_script "${SCRIPT_INSTALL_AUTHORINO}"
    
    # Export environment variables for Authorino script
    export NAMESPACE="${NAMESPACE}"
    
    if [[ "${VERBOSE}" == "true" ]]; then
        export VERBOSE="true"
    fi
    
    execute_script "${SCRIPT_INSTALL_AUTHORINO}"
    
    log_success "Authorino deployment completed"
}

deploy_helm_chart() {
    if [[ "${SKIP_HELM}" == "true" ]]; then
        log_warning "Skipping ROS Helm chart installation (--skip-helm)"
        return 0
    fi
    
    log_step "Step 4/6: Deploying ROS Helm chart with JWT authentication"
    
    download_script "${SCRIPT_INSTALL_HELM}"
    
    # Create custom values file for image override
    local values_file="${TEMP_DIR}/custom-values.yaml"
    create_helm_values_file "${values_file}"
    
    # Export environment variables for Helm script
    export NAMESPACE="${NAMESPACE}"
    export JWT_AUTH_ENABLED="true"
    export VALUES_FILE="${values_file}"
    export USE_LOCAL_CHART="${USE_LOCAL_CHART}"
    
    if [[ "${VERBOSE}" == "true" ]]; then
        export VERBOSE="true"
    fi
    
    execute_script "${SCRIPT_INSTALL_HELM}"
    
    log_success "ROS Helm chart deployment completed"
}

create_helm_values_file() {
    local values_file="$1"
    
    log_info "Creating custom Helm values file..."
    log_verbose "Values file: ${values_file}"
    
    cat > "${values_file}" <<EOF
# Custom values for ROS deployment
# Generated by deploy-ros-jwt.sh v${SCRIPT_VERSION}

# Image configuration
image:
  registry: ${IMAGE_REGISTRY}
  repository: ${IMAGE_REPOSITORY}
  tag: ${IMAGE_TAG}
  pullPolicy: Always

# JWT Authentication
jwtAuth:
  enabled: true

# Ingress configuration
ingress:
  enabled: true
  className: openshift-default
  annotations:
    route.openshift.io/termination: edge

# Resource limits
resources:
  limits:
    cpu: 1000m
    memory: 512Mi
  requests:
    cpu: 100m
    memory: 128Mi

# Autoscaling
autoscaling:
  enabled: true
  minReplicas: 2
  maxReplicas: 10
  targetCPUUtilizationPercentage: 80

# Health checks
livenessProbe:
  enabled: true
  initialDelaySeconds: 30
  periodSeconds: 10

readinessProbe:
  enabled: true
  initialDelaySeconds: 5
  periodSeconds: 5
EOF
    
    if [[ "${VERBOSE}" == "true" ]]; then
        log_verbose "Helm values file contents:"
        cat "${values_file}"
    fi
    
    log_success "Custom Helm values file created"
}

setup_tls() {
    if [[ "${SKIP_TLS}" == "true" ]]; then
        log_warning "Skipping TLS certificate setup (--skip-tls)"
        return 0
    fi
    
    log_step "Step 5/6: Configuring TLS certificates"
    
    download_script "${SCRIPT_SETUP_TLS}"
    
    # Export environment variables for TLS script
    export NAMESPACE="${NAMESPACE}"
    
    if [[ "${VERBOSE}" == "true" ]]; then
        export VERBOSE="true"
    fi
    
    execute_script "${SCRIPT_SETUP_TLS}"
    
    log_success "TLS certificate setup completed"
}

test_jwt_flow() {
    if [[ "${SKIP_TEST}" == "true" ]]; then
        log_warning "Skipping JWT authentication test (--skip-test)"
        return 0
    fi
    
    log_step "Step 6/6: Testing JWT authentication flow"
    
    download_script "${SCRIPT_TEST_JWT}"
    
    # Export environment variables for JWT test script
    export NAMESPACE="${NAMESPACE}"
    
    if [[ "${VERBOSE}" == "true" ]]; then
        export VERBOSE="true"
    fi
    
    execute_script "${SCRIPT_TEST_JWT}"
    
    log_success "JWT authentication test completed"
}

################################################################################
# Main deployment workflow
################################################################################

print_summary() {
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${CYAN}  DEPLOYMENT SUMMARY${NC}"
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo "  Namespace:           ${NAMESPACE}"
    echo "  Image:               ${IMAGE_REGISTRY}/${IMAGE_REPOSITORY}:${IMAGE_TAG}"
    echo "  Use Local Chart:     ${USE_LOCAL_CHART}"
    echo ""
    echo "  Steps to execute:"
    [[ "${SKIP_RHSSO}" == "false" ]] && echo "    ✓ Deploy RHSSO/Keycloak" || echo "    ✗ Deploy RHSSO/Keycloak (SKIPPED)"
    [[ "${SKIP_STRIMZI}" == "false" ]] && echo "    ✓ Deploy Kafka/Strimzi" || echo "    ✗ Deploy Kafka/Strimzi (SKIPPED)"
    [[ "${SKIP_AUTHORINO}" == "false" ]] && echo "    ✓ Deploy Authorino" || echo "    ✗ Deploy Authorino (SKIPPED)"
    [[ "${SKIP_HELM}" == "false" ]] && echo "    ✓ Deploy ROS Helm Chart" || echo "    ✗ Deploy ROS Helm Chart (SKIPPED)"
    [[ "${SKIP_TLS}" == "false" ]] && echo "    ✓ Setup TLS Certificates" || echo "    ✗ Setup TLS Certificates (SKIPPED)"
    [[ "${SKIP_TEST}" == "false" ]] && echo "    ✓ Test JWT Flow" || echo "    ✗ Test JWT Flow (SKIPPED)"
    echo ""
    echo -e "${CYAN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

print_completion() {
    echo ""
    echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo -e "${GREEN}  ✅ DEPLOYMENT COMPLETED SUCCESSFULLY${NC}"
    echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
    echo "  ROS with JWT authentication has been deployed to namespace: ${NAMESPACE}"
    echo ""
    echo "  Next steps:"
    echo "    1. Verify deployment status:"
    echo "       oc get pods -n ${NAMESPACE}"
    echo ""
    echo "    2. Check ROS ingress route:"
    echo "       oc get route -n ${NAMESPACE}"
    echo ""
    echo "    3. View logs:"
    echo "       oc logs -n ${NAMESPACE} -l app.kubernetes.io/name=ingress -f"
    echo ""
    echo "    4. Access ROS API:"
    echo "       curl -k https://\$(oc get route -n ${NAMESPACE} -o jsonpath='{.items[0].spec.host}')/api/ingress/v1/health"
    echo ""
    echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
    echo ""
}

main() {
    echo ""
    echo -e "${CYAN}╔════════════════════════════════════════════════════════════════════╗${NC}"
    echo -e "${CYAN}║  ROS OpenShift JWT Authentication Deployment Script v${SCRIPT_VERSION}      ║${NC}"
    echo -e "${CYAN}╚════════════════════════════════════════════════════════════════════╝${NC}"
    echo ""
    
    # Parse command line arguments
    while [[ $# -gt 0 ]]; do
        case $1 in
            --skip-rhsso)
                SKIP_RHSSO=true
                shift
                ;;
            --skip-strimzi)
                SKIP_STRIMZI=true
                shift
                ;;
            --skip-authorino)
                SKIP_AUTHORINO=true
                shift
                ;;
            --skip-helm)
                SKIP_HELM=true
                shift
                ;;
            --skip-tls)
                SKIP_TLS=true
                shift
                ;;
            --skip-test)
                SKIP_TEST=true
                shift
                ;;
            --namespace)
                NAMESPACE="$2"
                shift 2
                ;;
            --image-tag)
                IMAGE_TAG="$2"
                shift 2
                ;;
            --use-local-chart)
                USE_LOCAL_CHART=true
                shift
                ;;
            --verbose)
                VERBOSE=true
                shift
                ;;
            --dry-run)
                DRY_RUN=true
                shift
                ;;
            --help|-h)
                show_help
                ;;
            *)
                log_error "Unknown option: $1"
                echo "Use --help for usage information"
                exit 1
                ;;
        esac
    done
    
    # Show deployment summary
    print_summary
    
    if [[ "${DRY_RUN}" == "true" ]]; then
        log_warning "DRY RUN MODE: No changes will be made"
        echo ""
    fi
    
    # Execute deployment steps
    check_prerequisites
    check_oc_connection
    create_namespace
    
    deploy_rhsso
    deploy_strimzi
    deploy_authorino
    deploy_helm_chart
    setup_tls
    test_jwt_flow
    
    # Print completion message
    if [[ "${DRY_RUN}" == "false" ]]; then
        print_completion
    else
        echo ""
        log_info "DRY RUN completed. No changes were made."
        echo ""
    fi
}

# Run main function
main "$@"

