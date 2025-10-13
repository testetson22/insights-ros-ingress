# Insights ROS Ingress

A specialized ingress service for processing HCCM (Hybrid Cloud Cost Management) uploads and extracting Resource Optimization Service (ROS) data for on-premise deployments.

## Overview

This service combines the upload handling capabilities of `insights-ingress-go` with the ROS data extraction logic from `koku`, eliminating the need for koku as an intermediary. It processes OpenShift cluster payloads, extracts ROS CSV files, uploads them to MinIO storage, and notifies the ROS processor via Kafka.

## Architecture

```
HCCM Upload → insights-ros-ingress → MinIO (ROS bucket) → Kafka (ROS events) → ROS Processor
```

## Authentication Architecture

This service implements a **dual-server architecture** with selective authentication to support both intra-cluster monitoring and multi-cluster data ingestion scenarios.

### 🏗️ **Dual-Server Design**

| Port | Endpoints | Authentication | Use Case | Client Location |
|------|-----------|----------------|----------|-----------------|
| **8080** | `/upload` | ✅ **Sidecar-based** (Keycloak JWT) | Business API | External clusters |
| **8080** | `/health`, `/ready` | ❌ **Unprotected** | Pod lifecycle | Kubernetes |
| **9090** | `/metrics` | ✅ **OAuth2 TokenReviewer** | Observability | Same cluster |

### **Port 8080 - Main API Server (Sidecar Authentication)**

**Endpoints:**
- `POST /api/ingress/v1/upload` - HCCM data ingestion from external clusters
- `GET /health` - Health check endpoint (**Unprotected** - Kubernetes liveness probe)
- `GET /ready` - Readiness probe endpoint (**Unprotected** - Kubernetes readiness probe)

**Authentication Strategy:**
- **Upload endpoint**: Sidecar-based authentication with Keycloak JWT tokens
- **Health/Ready endpoints**: Always unprotected for Kubernetes pod lifecycle management
- **Sidecar pattern**: JWT validation handled by sidecar container (upload endpoint only)
- **Multi-cluster support** - external OpenShift clusters authenticate via JWT
- **Future extensibility** - authentication logic separated from business logic

**Rationale:**
```
# Upload endpoint flow:
External Cluster → Keycloak JWT → Sidecar (JWT Validation) → /upload (Port 8080)

# Health/Ready endpoint flow:
Kubernetes → /health, /ready (Port 8080) [Direct, no sidecar]
```
- **Multi-cluster authentication**: External clusters use Keycloak JWT tokens for upload
- **Sidecar pattern**: Simplifies Keycloak JWT integration and token validation
- **Pod lifecycle**: Health/Ready endpoints bypass sidecar for Kubernetes probes
- **Provider flexibility**: Easy to swap authentication providers without code changes
- **Business logic isolation**: Authentication concerns separated from upload processing

### **Port 9090 - Metrics Server (OAuth2 TokenReviewer)**

**Endpoints:**
- `GET /metrics` - Prometheus metrics for monitoring

**Authentication Strategy:**
- **Kubernetes OAuth2 TokenReviewer** - native K8s authentication
- **Service account tokens** - standard Kubernetes RBAC
- **Intra-cluster only** - designed for same-cluster Prometheus scraping

**Rationale:**
```
Prometheus (Same Cluster) → K8s Service Account Token → TokenReviewer API → Metrics (Port 9090)
```
- **Native Kubernetes security**: Leverages built-in K8s authentication mechanisms
- **RBAC integration**: Standard Kubernetes permissions for monitoring services
- **Cluster-local**: No external authentication complexity needed
- **Production monitoring**: Enterprise-grade security for observability endpoints

### 🎯 **Development Guidelines**

#### **For Business API Development (Port 8080):**
```bash
# Upload endpoint - Local development (no sidecar)
curl http://localhost:8080/api/ingress/v1/upload

# Upload endpoint - Production (JWT handled by sidecar)
# Application receives pre-authenticated requests from sidecar

# Health/Ready endpoints - Always unprotected (local & production)
curl http://localhost:8080/health    # Kubernetes liveness probe
curl http://localhost:8080/ready     # Kubernetes readiness probe
```

#### **Sidecar Authentication Example (Envoy + Authorino):**

**Deployment Architecture:**
```yaml
# Pod contains both Envoy sidecar and application containers
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
        # Envoy Proxy Sidecar (intercepts traffic)
        - name: envoy-proxy
          image: envoyproxy/envoy:v1.24.1
          ports:
            - containerPort: 8080  # Public-facing port
            - containerPort: 9901  # Admin port
          volumeMounts:
            - name: envoy-config
              mountPath: /etc/envoy

        # Application Container (receives authenticated requests)
        - name: ingress
          image: insights-ros-ingress:latest
          ports:
            - containerPort: 8081  # Internal port (behind Envoy)
          env:
            - name: SERVER_PORT
              value: "8081"       # Different from Envoy port
            - name: AUTH_ENABLED
              value: "false"      # App-level auth disabled (handled by sidecar)
```

**Envoy Configuration (envoy.yaml):**
```yaml
static_resources:
  listeners:
  - name: listener_0
    address:
      socket_address: { address: 0.0.0.0, port_value: 8080 }
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          http_filters:
          # External authorization filter - calls Authorino
          - name: envoy.filters.http.ext_authz
            typed_config:
              grpc_service:
                envoy_grpc:
                  cluster_name: authorino-service
              failure_mode_allow: false  # Fail closed

          # Standard HTTP router
          - name: envoy.filters.http.router
          route_config:
            virtual_hosts:
            - name: ros_ingress_backend
              domains: ["*"]
              routes:
              - match: { prefix: "/" }
                route:
                  cluster: ros-ingress-backend

  clusters:
  # Authorino external authorization service
  - name: authorino-service
    type: LOGICAL_DNS
    load_assignment:
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: ros-authorino-custom-authorization.ros-ocp.svc.cluster.local
                port_value: 50051

  # Backend application (same pod, different port)
  - name: ros-ingress-backend
    type: LOGICAL_DNS
    load_assignment:
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address:
                address: localhost  # Same pod
                port_value: 8081    # Application port
```

**Authorino AuthConfig (Keycloak JWT):**
```yaml
apiVersion: authorino.kuadrant.io/v1beta3
kind: AuthConfig
metadata:
  name: ros-ingress-jwt-auth
spec:
  hosts:
  - "ros-ingress.ros-ocp.svc.cluster.local"

  # JWT Authentication with Keycloak
  authentication:
    "keycloak-jwt":
      jwt:
        issuerUrl: "https://keycloak.example.com/auth/realms/ros-realm"
      credentials:
        authorizationHeader:
          prefix: Bearer

  # Authorization - allow authenticated users
  authorization:
    "allow-authenticated":
      patternMatching:
        patterns:
        - selector: auth.identity.sub
          operator: neq
          value: ""

  # Forward authentication context to backend
  response:
    success:
      headers:
        "X-ROS-Authenticated":
          plain: { value: "true" }
        "X-ROS-User-ID":
          plain: { selector: auth.identity.sub }
        "X-Bearer-Token":
          plain: { selector: context.request.http.headers.authorization }
```

**Authentication Flow:**

*Upload Endpoint (with JWT validation):*
```
1. Client → POST /api/ingress/v1/upload + Authorization: Bearer <jwt>
2. Envoy → Intercepts request at port 8080
3. Envoy → Calls Authorino via gRPC: "Validate this JWT"
4. Authorino → Validates JWT against Keycloak issuer
5. Authorino → Returns: "Valid user + headers"
6. Envoy → Forwards to localhost:8081 with auth headers:
   - X-ROS-Authenticated: true
   - X-ROS-User-ID: user123
   - X-Bearer-Token: <original-jwt>
7. Application → Processes upload (no auth logic needed)
```

*Health/Ready Endpoints (bypass sidecar):*
```
1. Kubernetes → GET /health (or /ready) directly to port 8081
2. Application → Responds immediately (no Envoy/Authorino involved)
3. Pod lifecycle management continues normally
```

**Note**: Health and readiness probes must bypass the sidecar to prevent authentication dependencies during pod lifecycle management. The Envoy configuration can be set to exclude certain paths, or probes can target the application port directly.

**Complete Implementation**: See the [ros-helm-chart](https://github.com/insights-onprem/ros-helm-chart) repository for the complete Envoy + Authorino deployment templates, including:
- `templates/deployment-ingress.yaml` - Sidecar deployment configuration
- `templates/envoy-config.yaml` - Envoy proxy configuration
- `templates/authconfig.yaml` - Authorino JWT authentication rules
- `docs/README-JWT-AUTH.md` - Complete setup instructions

#### **For Monitoring Setup (Port 9090):**
```yaml
# Prometheus scrape config
- job_name: 'insights-ros-ingress-metrics'
  kubernetes_sd_configs:
  - role: pod
  scheme: http
  metrics_path: /metrics
  static_configs:
  - targets: ['insights-ros-ingress:9090']
  # Uses K8s service account token automatically
```

#### **Authentication Flow Examples:**

**External Client Upload (Port 8080 /upload):**
1. Client obtains **Keycloak JWT token**
2. HTTP request: `Authorization: Bearer <jwt-token>` to `/api/ingress/v1/upload`
3. **Sidecar validates JWT** against Keycloak
4. **Application receives authenticated request** (no auth logic needed)
5. Upload processing continues normally

**Kubernetes Health Checks (Port 8080 /health, /ready):**
1. Kubernetes sends probe requests **directly to application** (bypasses sidecar)
2. **No authentication required** - immediate response
3. Pod lifecycle management continues normally

**Prometheus Metrics Collection (Port 9090):**
1. Prometheus uses **Kubernetes service account token**
2. HTTP request: `Authorization: Bearer <k8s-sa-token>`
3. **TokenReviewer validates** service account permissions
4. **Application serves metrics** to authenticated monitoring service

### 🔒 **Security Benefits**

- ✅ **Clear security boundaries** between business and observability APIs
- ✅ **Multi-cluster authentication** support via JWT tokens
- ✅ **Native Kubernetes integration** for monitoring workflows
- ✅ **Provider flexibility** - easy to change authentication mechanisms
- ✅ **Business logic isolation** - authentication handled externally
- ✅ **Zero trust architecture** - both external and internal access properly secured

## Features

- **HCCM Upload Processing**: Handles `application/vnd.redhat.hccm.upload` content-type
- **Payload Extraction**: Extracts and validates tar.gz payloads with manifest.json
- **ROS File Processing**: Identifies and processes resource optimization CSV files
- **MinIO Integration**: S3-compatible storage for on-premise deployments
- **Kafka Integration**: Sends events to `hccm.ros.events` topic
- **OpenShift Deployment**: Helm chart with native OpenShift support
- **Cloud-Native**: Designed for Kubernetes without Clowder dependency

## Quick Start

### Using Podman Compose

```bash
# Start dependencies
podman-compose -f deployments/docker-compose/docker-compose.yml up -d

# Build and run
make build
make run
```

### Using Helm on Kubernetes/OpenShift

The Helm chart is pulled from the [ros-helm-chart](https://github.com/insights-onprem/ros-helm-chart) repository:

```bash
# Deploy using the installation script (recommended)
./deployments/kubernetes/scripts/install-helm-chart.sh

# Or manually with latest release from GitHub
# The script automatically downloads and installs the latest chart
```

## Configuration

The service uses Kubernetes ConfigMaps and Secrets for configuration, mimicking Clowder behavior without the dependency:

- **ConfigMaps**: Application configuration
- **Secrets**: MinIO and Kafka credentials
- **Environment Variables**: Service discovery endpoints

## Development

### Prerequisites

- Go 1.24.4+
- Podman 4.0+
- Make

### Building

```bash
# Build binary
make build

# Build container image
make build-image

# Run tests
make test

# Run linting
make lint
```

## Project Structure

```
├── cmd/insights-ros-ingress/    # Main application entry point
├── internal/                   # Private application code
│   ├── config/                 # Configuration management
│   ├── upload/                 # HTTP upload handlers
│   ├── storage/                # MinIO storage client
│   ├── messaging/              # Kafka producer
│   ├── logger/                 # Logging utilities
│   └── health/                 # Health check endpoints
├── deployments/                # Deployment configurations
│   ├── kubernetes/helm/        # Helm charts for Kubernetes/OpenShift
│   └── docker-compose/         # Docker Compose for development
├── docs/                       # Documentation
└── configs/                    # Configuration files
```

## API Endpoints

### Port 8080 - Main API Server
- `POST /api/ingress/v1/upload` - Upload HCCM payload (Sidecar authentication with Keycloak JWT)
- `GET /health` - Health check (Unprotected - Kubernetes liveness probe)
- `GET /ready` - Readiness probe (Unprotected - Kubernetes readiness probe)

### Port 9090 - Metrics Server
- `GET /metrics` - Prometheus metrics (OAuth2 TokenReviewer authentication)

## Testing

### Unit Tests
```bash
make test
```

### Integration Testing

Complete end-to-end integration test with docker-compose services:

```bash
# Full integration test (recommended)
make test-integration

# Quick test with existing services
make dev-env-up
make test-integration-quick

# Manual testing
make dev-env-up
make run-test  # In one terminal
# Test upload API in another terminal
```

The integration test validates:
- Docker-compose services (MinIO, Kafka, Zookeeper)
- ROS data upload and processing
- MinIO file storage and metadata
- Kafka message publishing

For detailed testing instructions, see [docs/testing.md](docs/testing.md).

## Troubleshooting

### Kafka Cluster ID Mismatch

If Kafka fails to start with an `InconsistentClusterIdException`, this indicates that Kafka has cached metadata from a previous Zookeeper cluster:

```
kafka.common.InconsistentClusterIdException: The Cluster ID doesn't match stored clusterId
```

**Solution**: Clear the data volumes to remove cached metadata:

```bash
# Stop all services
podman-compose -f deployments/docker-compose/docker-compose.yml down

# Clear all data volumes
podman volume prune -f

# Restart services with fresh data
podman-compose -f deployments/docker-compose/docker-compose.yml up -d
```

### Port Already in Use

If you encounter `bind: address already in use` errors when running `make run-dev`:

**Solution**: Kill any existing instances of the service:

```bash
# Find and kill any running insights-ros-ingress processes
pkill -f insights-ros-ingress

# Or check what's using the port
lsof -i :8080

# Then run the service again
make run-dev
```

### Service Dependencies

Ensure all required services are running before starting the application:

```bash
# Check service status
podman-compose -f deployments/docker-compose/docker-compose.yml ps

# View service logs if needed
podman-compose -f deployments/docker-compose/docker-compose.yml logs -f kafka
```

## Contributing

Please follow the guidelines in [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache License 2.0