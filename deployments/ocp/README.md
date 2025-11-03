# OpenShift Deployment for ROS Ingress

Automated deployment script for ROS Ingress on OpenShift with JWT authentication.

## Quick Start

```bash
# Set credentials (if not already in environment)
export KUBECONFIG=/path/to/kubeconfig
export KUBEADMIN_PASSWORD_FILE=/path/to/kubeadmin-password
# OR
export SHARED_DIR=/path/to/shared  # Contains kubeadmin-password

# Deploy
./deploy-ros-jwt.sh --image-tag main-abc123
```

## Features

- **Automatic Login**: Auto-detects credentials from kubeconfig and password files
- **Modular Steps**: Skip individual deployment steps with flags
- **Custom Images**: Deploy specific image tags from CI/CD
- **Dry-Run Mode**: Preview actions without making changes
- **Smart Detection**: Checks if already logged in, skips if authenticated

## Prerequisites

**Required:**
- `oc` CLI
- `helm` (v3+)
- `yq` (for YAML/JSON processing and credential detection)
- `curl`

## Deployment Steps

The script deploys in this order (each can be skipped):

1. RHSSO/Keycloak (`--skip-rhsso`)
2. Kafka/Strimzi (`--skip-strimzi`)
3. Authorino OAuth2 (`--skip-authorino`)
4. ROS Helm Chart (`--skip-helm`)
5. TLS Certificates (`--skip-tls`)
6. JWT Flow Test (`--skip-test`)

## Command-Line Options

```bash
./deploy-ros-jwt.sh [OPTIONS]

--skip-rhsso           Skip RHSSO/Keycloak deployment
--skip-strimzi         Skip Kafka/Strimzi deployment
--skip-authorino       Skip Authorino OAuth2 deployment
--skip-helm            Skip ROS Helm chart installation
--skip-tls             Skip TLS certificate setup
--skip-test            Skip JWT authentication test
--namespace NAME       Target namespace (default: ros-ocp)
--image-tag TAG        Custom image tag
--use-local-chart      Use local Helm chart
--verbose              Enable verbose output
--dry-run              Preview without executing
--help                 Show help message
```

## Environment Variables

### Authentication (Auto-Detected)
```bash
KUBECONFIG               # Path to kubeconfig (default: ~/.kube/config)
KUBEADMIN_PASSWORD_FILE  # Path to kubeadmin password file
SHARED_DIR               # Directory containing kubeadmin-password
OPENSHIFT_API            # API URL (auto-detected from kubeconfig)
OPENSHIFT_USERNAME       # Username (default: kubeadmin)
OPENSHIFT_PASSWORD       # Password (auto-detected from files)
```

### Image Configuration
```bash
IMAGE_REGISTRY           # Image registry (default: quay.io)
IMAGE_REPOSITORY         # Repository (default: insights-onprem/insights-ros-ingress)
IMAGE_TAG                # Image tag (default: latest)
```

### Deployment Options
```bash
NAMESPACE                # Target namespace (default: ros-ocp)
USE_LOCAL_CHART          # Use local chart (default: false)
VERBOSE                  # Verbose output (default: false)
DRY_RUN                  # Dry-run mode (default: false)
```

## Common Usage Patterns

### Full Deployment
```bash
./deploy-ros-jwt.sh --image-tag main-abc123 --verbose
```

### Update Only ROS Application
```bash
./deploy-ros-jwt.sh \
    --skip-rhsso \
    --skip-strimzi \
    --skip-authorino \
    --skip-tls \
    --skip-test \
    --image-tag main-xyz789
```

### Dry Run
```bash
./deploy-ros-jwt.sh --dry-run --verbose
```

### CI/CD Integration
```bash
# GitHub Actions example
export KUBECONFIG="${KUBECONFIG}"
export KUBEADMIN_PASSWORD_FILE="${KUBEADMIN_PASSWORD_FILE}"
export IMAGE_TAG="main-${GITHUB_SHA}"

./deployments/ocp/deploy-ros-jwt.sh \
    --image-tag "${IMAGE_TAG}" \
    --namespace ros-ocp \
    --skip-test
```

## Credential Detection

The script automatically detects credentials in this priority order:

**API URL:**
1. `OPENSHIFT_API` environment variable
2. Auto-detected from `KUBECONFIG` using `yq`

**Password:**
1. `OPENSHIFT_PASSWORD` environment variable
2. `KUBEADMIN_PASSWORD_FILE`
3. `SHARED_DIR/kubeadmin-password`

**Behavior:**
- Checks if already logged in first
- Only attempts login if not authenticated
- Falls back to manual instructions if auto-login fails

## Troubleshooting

### Check Prerequisites
```bash
which oc helm yq curl
```

### Verify Connection
```bash
oc whoami
oc cluster-info
```

### Check Deployment
```bash
# Pod status
oc get pods -n ros-ocp

# View logs
oc logs -n ros-ocp -l app.kubernetes.io/name=ingress -f

# Check events
oc get events -n ros-ocp --sort-by='.lastTimestamp'

# Verify route
oc get route -n ros-ocp
ROUTE_HOST=$(oc get route -n ros-ocp -o jsonpath='{.items[0].spec.host}')
curl -k "https://${ROUTE_HOST}/api/ingress/v1/health"
```

### Resume Failed Deployment
```bash
# Skip already-deployed components
./deploy-ros-jwt.sh --skip-rhsso --skip-strimzi
```

### Manual Login
```bash
# If auto-login fails
oc login https://api.example.com:6443
./deploy-ros-jwt.sh
```

## Architecture

```
┌─────────────────────────────────────────┐
│         OpenShift Cluster               │
│                                         │
│  ┌────────────────────────────────┐    │
│  │  Namespace: ros-ocp             │    │
│  │                                  │    │
│  │  RHSSO/Keycloak                 │    │
│  │       ↓                          │    │
│  │  Authorino (OAuth2)             │    │
│  │       ↓                          │    │
│  │  ROS Ingress (JWT + Envoy)      │    │
│  │       ↓                          │    │
│  │  Kafka (Strimzi)                │    │
│  │                                  │    │
│  └────────────────────────────────┘    │
│                                         │
│  Cost Management Metrics Operator       │
│                                         │
└─────────────────────────────────────────┘
```

## Testing

Run the following script to essentially execute a dry-run validation of the `deploy-ros-jwt.sh` script:

```bash
./test-script.sh
```

All 17 tests validate:
- Script syntax
- Command-line parsing
- Help output
- Dry-run mode
- Environment variables
- Error handling

## Related Documentation

- [ROS Helm Chart Scripts](https://github.com/insights-onprem/ros-helm-chart/blob/main/scripts/README.md)
- [JWT Authentication Guide](https://github.com/insights-onprem/ros-helm-chart/blob/main/docs/native-jwt-authentication.md)
- [Installation Guide](https://github.com/insights-onprem/ros-helm-chart/blob/main/docs/installation.md)
