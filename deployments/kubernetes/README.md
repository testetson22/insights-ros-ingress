# Insights ROS Ingress Kubernetes Deployment

This directory contains Kubernetes deployment resources for the Insights ROS Ingress service, including Helm charts and deployment scripts for local development and testing.

## Overview

The Insights ROS Ingress service processes file uploads and validates them for the Red Hat Insights Resource Optimization Service (ROS). This Kubernetes deployment includes all necessary dependencies for a complete, functional environment.

## Components

### Main Service
- **insights-ros-ingress**: The main ingress service that handles file uploads, validation, and processing

### Dependencies
- **MinIO**: S3-compatible object storage for file storage
- **Kafka**: Message streaming platform for event processing
- **Zookeeper**: Coordination service required by Kafka

## Directory Structure

```
deployments/kubernetes/
├── scripts/
│   ├── test-k8s-dataflow.sh        # Kubernetes dataflow testing script (local)
│   ├── update-kind-image.sh        # Build and deploy local code changes to KIND (local)
│   └── cleanup-kind-artifacts.sh   # Cleanup script for KIND (local)
└── README.md                       # This file
```

**Important Notes:**
- The Helm chart is maintained in a separate repository: [insights-onprem/ros-helm-chart](https://github.com/insights-onprem/ros-helm-chart)
- Deployment scripts (`deploy-kind.sh`, `install-helm-chart.sh`) are **always downloaded fresh** from the ros-helm-chart repository via Makefile targets to ensure you're using the latest authoritative versions
- Use `make deploy-kind` and `make helm-install` instead of running scripts directly

## Quick Start

### Prerequisites

Install the following tools:
- [Podman](https://podman.io/) - Container engine (recommended, daemonless)
- [KIND](https://kind.sigs.k8s.io/) - Kubernetes in Docker/Podman
- [kubectl](https://kubernetes.io/docs/tasks/tools/) - Kubernetes CLI
- [Helm](https://helm.sh/) - Kubernetes package manager

**macOS Installation:**
```bash
brew install podman kind kubectl helm
```

**Note:** This project uses Podman by default instead of Docker for better security (daemonless, rootless) and compatibility. Docker can be used as an alternative by setting `CONTAINER_RUNTIME=docker`.

### Deploy to KIND

#### Option 1: Full Development Setup (Recommended for Development)

**Quick Start - One Command Setup:**
```bash
make deploy-dev
```

This single command will:
1. Download and run latest `deploy-kind.sh` to create KIND cluster
2. Download and run latest `install-helm-chart.sh` to install Helm chart
3. **Build container image from your current workspace code**
4. **Load the image into KIND cluster**
5. **Patch the deployment to use your local code instead of quay.io**

After this completes, your KIND cluster will be running with your latest local code changes!

#### Option 2: Manual Step-by-Step Setup

If you prefer more control over each step:

1. **Create KIND cluster:**
   ```bash
   make deploy-kind
   ```

2. **Install Helm chart:**
   ```bash
   make helm-install
   ```

3. **Update with local code (optional but recommended for development):**
   ```bash
   make update-kind-image
   ```

#### Verify Deployment

1. **Check deployment status:**
   ```bash
   make helm-status
   ```

2. **Run health checks:**
   ```bash
   make helm-health
   ```

3. **Test the complete dataflow:**
   ```bash
   ./deployments/kubernetes/scripts/test-k8s-dataflow.sh
   ```

### Access Points

After deployment, the following services are available:

- **Insights ROS Ingress API**: http://localhost:30080
  - Health: http://localhost:30080/health
  - Ready: http://localhost:30080/ready
  - Metrics: http://localhost:30080/metrics (requires auth)
  - Upload: http://localhost:30080/api/ingress/v1/upload

- **MinIO Console**: http://localhost:32061
  - Username: `minioadmin`
  - Password: `minioadmin123`

- **MinIO S3 API**: http://localhost:32061

## Configuration

### Helm Values

The Helm chart is pulled from GitHub. You can customize the deployment by providing a custom values file:

```bash
# Create a custom values file
cat > my-values.yaml <<EOF
image:
  tag: v1.2.3
replicaCount: 3
minio:
  persistence:
    size: 50Gi
EOF

# Deploy with custom values
VALUES_FILE=my-values.yaml make helm-install
```

### Environment Variables

Key environment variables for the deployment script:

- `HELM_RELEASE_NAME`: Name of the Helm release (default: `ros-ocp`)
- `NAMESPACE`: Kubernetes namespace (default: `ros-ocp`)
- `VALUES_FILE`: Path to custom values file (optional)
- `USE_LOCAL_CHART`: Use local chart instead of GitHub release (default: `false`)
- `LOCAL_CHART_PATH`: Path to local chart directory (default: `../helm/ros-ocp`)

### Image Configuration

To use a custom image, create a values file:

```bash
cat > custom-image-values.yaml <<EOF
image:
  repository: your-registry/insights-ros-ingress
  tag: your-tag
EOF

VALUES_FILE=custom-image-values.yaml make helm-install
```

## Development Workflow

### Local Testing

1. **Build and test locally:**
   ```bash
   # Build the application
   make build

   # Run unit tests
   make test
   ```

2. **Deploy to KIND:**
   ```bash
   make deploy-kind
   ```

3. **Test local code changes in KIND:**
   ```bash
   # Build container image from current workspace and update KIND deployment
   make update-kind-image

   # This will:
   # - Build container image from your current code
   # - Load it into the KIND cluster
   # - Patch the deployment to use the new image
   # - Wait for rollout to complete
   ```

4. **Test the deployment:**
   ```bash
   ./deployments/kubernetes/scripts/test-k8s-dataflow.sh
   ```

5. **View logs:**
   ```bash
   kubectl logs -n ros-ocp -l app.kubernetes.io/name=insights-ros-ingress -f
   ```

### Iterative Development Workflow

For rapid development and testing:

```bash
# 1. Initial setup (once) - sets up KIND with your current code
make deploy-dev

# 2. Make code changes
# ... edit your code ...

# 3. Update running deployment with your changes
make update-kind-image

# 4. Test your changes
./deployments/kubernetes/scripts/test-k8s-dataflow.sh

# 5. View logs if needed
kubectl logs -n ros-ocp -l app.kubernetes.io/name=insights-ros-ingress -f

# 6. Repeat steps 2-5 as needed
```

**Why `make deploy-dev`?**
- ✅ Automatically uses your local code instead of pulling from quay.io
- ✅ Faster iteration - no need to push images to remote registry
- ✅ Test your changes immediately in a Kubernetes environment
- ✅ Complete setup in one command

**Advanced Options:**

```bash
# Use custom image tag
IMAGE_TAG=my-feature make update-kind-image

# Use custom namespace
NAMESPACE=my-namespace make update-kind-image

# Skip building (if image already exists)
./deployments/kubernetes/scripts/update-kind-image.sh --skip-build

# Only show current deployment status
./deployments/kubernetes/scripts/update-kind-image.sh --status-only

# Use Docker instead of Podman (if needed)
# Note: Podman is the default and recommended container runtime
CONTAINER_RUNTIME=docker make update-kind-image
```

### Updating the Deployment

1. **Update to latest Helm chart:**
   ```bash
   # Always pulls the latest authoritative script and chart from ros-helm-chart
   make helm-install
   ```

2. **Update with custom image version:**
   ```bash
   cat > update-values.yaml <<EOF
   image:
     tag: new-version
   EOF

   VALUES_FILE=update-values.yaml make helm-install
   ```

3. **Rolling restart:**
   ```bash
   kubectl rollout restart deployment -n ros-ocp -l app.kubernetes.io/instance=ros-ocp
   ```

## Testing

### Automated Testing

The repository includes GitHub Actions workflows (see `.github/workflows/`) that automatically:

- Run unit and integration tests
- Build and push container images
- Deploy to KIND clusters for testing
- Run dataflow tests
- Perform security scans

### Manual Testing

1. **Health checks:**
   ```bash
   ./deployments/kubernetes/scripts/test-k8s-dataflow.sh health
   ```

2. **Upload API:**
   ```bash
   ./deployments/kubernetes/scripts/test-k8s-dataflow.sh upload
   ```

3. **Storage verification:**
   ```bash
   ./deployments/kubernetes/scripts/test-k8s-dataflow.sh storage
   ```

4. **Kafka verification:**
   ```bash
   ./deployments/kubernetes/scripts/test-k8s-dataflow.sh kafka
   ```

## Troubleshooting

### Common Issues

1. **Pods not starting:**
   ```bash
   kubectl get pods -n insights-ros-ingress
   kubectl describe pod <pod-name> -n insights-ros-ingress
   kubectl logs <pod-name> -n insights-ros-ingress
   ```

2. **Storage issues:**
   ```bash
   kubectl get pvc -n insights-ros-ingress
   kubectl get storageclass
   ```

3. **Network connectivity:**
   ```bash
   kubectl get services -n insights-ros-ingress
   kubectl port-forward -n insights-ros-ingress svc/insights-ros-ingress 8080:8080
   ```

### Debug Commands

```bash
# Check all resources
kubectl get all -n insights-ros-ingress

# View events
kubectl get events -n insights-ros-ingress --sort-by='.lastTimestamp'

# Check Helm release
helm status insights-ros-ingress -n insights-ros-ingress

# View Helm values
helm get values insights-ros-ingress -n insights-ros-ingress
```

## Cleanup

### Remove Deployment

```bash
# Remove Helm release only (preserves PVs)
make helm-cleanup

# Complete cleanup including Persistent Volumes
# Note: Pass arguments by downloading script directly or use kubectl
kubectl delete namespace ros-ocp

# Remove entire KIND cluster
kind delete cluster --name ros-ocp-cluster
```

### Manual Cleanup

```bash
# Uninstall Helm release
helm uninstall ros-ocp -n ros-ocp

# Delete namespace
kubectl delete namespace ros-ocp

# Delete KIND cluster
kind delete cluster --name ros-ocp-cluster
```

## Production Considerations

When deploying to production environments:

1. **Security**:
   - Use proper authentication and authorization
   - Enable TLS for all communications
   - Use Kubernetes secrets for sensitive data

2. **Persistence**:
   - Use appropriate storage classes for production workloads
   - Configure backup strategies for persistent data

3. **Monitoring**:
   - Enable ServiceMonitor for Prometheus
   - Configure proper logging aggregation
   - Set up alerting for critical metrics

4. **Scaling**:
   - Adjust replica counts based on load
   - Configure horizontal pod autoscaling
   - Size persistent volumes appropriately

5. **Updates**:
   - Use rolling updates for zero-downtime deployments
   - Test updates in staging environments first
   - Have rollback procedures ready

## Contributing

When contributing to the Kubernetes deployment:

1. Test changes locally using KIND
2. Update documentation for any configuration changes
3. Ensure GitHub Actions tests pass
4. Follow Helm best practices for chart development

## Support

For issues and questions:
- Check the troubleshooting section above
- Review logs for error messages
- Open an issue in the project repository