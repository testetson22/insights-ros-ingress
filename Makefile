# Insights ROS Ingress Makefile
# Following CLAUDE.md guidelines for using podman instead of docker

# Variables
APP_NAME := insights-ros-ingress
VERSION ?= latest
IMAGE_REPO ?= quay.io/insights-onprem/$(APP_NAME)
IMAGE_TAG ?= $(VERSION)
IMAGE_NAME := $(APP_NAME):$(VERSION)
REGISTRY ?= quay.io/insights-onprem

# Go variables
GO_VERSION := 1.21
GOOS ?= linux
GOARCH ?= amd64
CGO_ENABLED ?= 1

# Build directories
BUILD_DIR := build
BIN_DIR := $(BUILD_DIR)/bin

# Go flags
LDFLAGS := -w -s -X main.version=$(VERSION)
GO_BUILD_FLAGS := -ldflags "$(LDFLAGS)"

.PHONY: help
help: ## Display this help message
	@echo "Available targets:"
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

.PHONY: clean
clean: clean-mocks clean-build ## Clean build artifacts
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)

.PHONY: clean-build
clean-build: ## Remove Podman images
	@echo "Cleaning Podman images..."
	podman rmi $(IMAGE_REPO):$(IMAGE_TAG) 2>/dev/null || true
	podman rmi $(IMAGE_REPO):$(IMAGE_TAG)-arm64 2>/dev/null || true
	podman rmi $(IMAGE_REPO):$(IMAGE_TAG)-amd64 2>/dev/null || true

.PHONY: deps
deps: ## Download and verify dependencies
	@echo "Downloading dependencies..."
	go mod download
	go mod verify
	go mod tidy

.PHONY: fmt
fmt: ## Format Go code
	@echo "Formatting Go code..."
	go fmt ./...
	goimports -w .

.PHONY: lint
lint: ## Run linters
	@echo "Running linters..."
	golangci-lint run ./...

.PHONY: vet
vet: ## Run go vet
	@echo "Running go vet..."
	go vet ./...

.PHONY: test
test: ## Run tests in containerized environment
	@echo "Running tests in containerized environment..."
	podman build -f Dockerfile.tests -t $(APP_NAME):test .
	@echo "✅ All tests passed in container!"

.PHONY: test-local
test-local: mocks ## Run tests locally (without container)
	@echo "Running tests locally..."
	go test -v -race -coverprofile=coverage.out ./...

.PHONY: test-coverage
test-coverage: test-local ## Run tests locally and generate coverage report
	@echo "Generating coverage report..."
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"

.PHONY: mocks
mocks: install-tools ## Generate mocks for testing
	@echo "Ensuring mocks exist..."
	@mkdir -p internal/auth/mocks
	@if [ ! -f internal/auth/mocks/mock_k8s_auth.go ]; then \
		echo "Generating mocks with go generate..."; \
		cd internal/auth/ && \
		GOFLAGS=-mod=mod go generate ./generate.go; \
	fi
	@echo "Mocks ready in internal/auth/mocks/"

.PHONY: clean-mocks
clean-mocks: ## Remove generated mocks
	@echo "Cleaning generated mocks..."
	rm -f internal/auth/mocks/mock_k8s_auth.go
	@echo "Mocks cleaned"

.PHONY: build
build: clean mocks deps  ## Build the binary
	@echo "Building $(APP_NAME)..."
	mkdir -p $(BIN_DIR)
ifeq ($(CGO_ENABLED),1)
	@echo "Building with CGO enabled for current platform..."
	CGO_ENABLED=$(CGO_ENABLED) go build \
		$(GO_BUILD_FLAGS) \
		-o $(BIN_DIR)/$(APP_NAME) \
		./cmd/$(APP_NAME)
else
	@echo "Building with CGO disabled for $(GOOS)/$(GOARCH)..."
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) go build \
		$(GO_BUILD_FLAGS) \
		-o $(BIN_DIR)/$(APP_NAME) \
		./cmd/$(APP_NAME)
endif
	@echo "Binary built: $(BIN_DIR)/$(APP_NAME)"


.PHONY: build-image
build-image: ## Build container image using podman
	@echo "Building container image with podman..."
	podman build --platform=linux/amd64 -t $(IMAGE_REPO):$(IMAGE_TAG) .
	@echo "Image built: $(IMAGE_REPO):$(IMAGE_TAG)"

.PHONY: build-image-arm64
build-image-arm64: ## Build container image for arm64 architecture
	@echo "Building container image for arm64..."
	podman build --platform=linux/arm64 -t $(IMAGE_REPO):$(IMAGE_TAG)-arm64 .
	@echo "ARM64 image built: $(IMAGE_REPO):$(IMAGE_TAG)-arm64"

.PHONY: build-image-amd64
build-image-amd64: ## Build container image for amd64 architecture
	@echo "Building container image for amd64..."
	podman build --platform=linux/amd64 -t $(IMAGE_REPO):$(IMAGE_TAG)-amd64 .
	@echo "AMD64 image built: $(IMAGE_REPO):$(IMAGE_TAG)-amd64"

.PHONY: build-image-push
build-image-push: build-image ## Push container image to registry
	@echo "Pushing image to registry..."
	podman push $(IMAGE_REPO):$(IMAGE_TAG)

.PHONY: build-image-push-arm64
build-image-push-arm64: build-image-arm64 ## Push ARM64 container image to registry
	@echo "Pushing ARM64 image to registry..."
	podman push $(IMAGE_REPO):$(IMAGE_TAG)-arm64

.PHONY: build-image-push-amd64
build-image-push-amd64: build-image-amd64 ## Push AMD64 container image to registry
	@echo "Pushing AMD64 image to registry..."
	podman push $(IMAGE_REPO):$(IMAGE_TAG)-amd64

.PHONY: run
run: build ## Run the application locally
	@echo "Running $(APP_NAME)..."
	./$(BIN_DIR)/$(APP_NAME)

.PHONY: run-test
run-test: build ## Run the application locally with test configuration
	@echo "Running $(APP_NAME) with test configuration..."
	@if [ -f configs/local-test.env ]; then \
		export $$(cat configs/local-test.env | grep -v '^#' | xargs) && ./$(BIN_DIR)/$(APP_NAME); \
	else \
		echo "Test configuration not found. Run: make test-env-up first"; \
		exit 1; \
	fi

.PHONY: run-dev
run-dev: build ## Run the application locally with development configuration
	@echo "Running $(APP_NAME) with development configuration..."
	@if [ -f configs/local-dev.env ]; then \
		export $$(cat configs/local-dev.env | grep -v '^#' | xargs) && ./$(BIN_DIR)/$(APP_NAME); \
	else \
		echo "Development configuration not found. Run: make dev-env-up first"; \
		exit 1; \
	fi

.PHONY: dev-env-up
dev-env-up: ## Start development environment with KIND cluster and podman-compose
	@echo "Starting development environment..."
	@echo "Setting up KIND cluster for authentication..."
	chmod +x deployments/docker-compose/scripts/setup-dev-auth.sh
	./deployments/docker-compose/scripts/setup-dev-auth.sh
	@echo "Starting services with podman-compose..."
	podman-compose -f deployments/docker-compose/docker-compose.yml up -d

.PHONY: dev-env-down
dev-env-down: ## Stop development environment and KIND cluster
	@echo "Stopping development environment..."
	podman-compose -f deployments/docker-compose/docker-compose.yml down
	@echo "Stopping KIND cluster..."
	-kind delete cluster --name insights-dev 2>/dev/null || true

.PHONY: test-integration
test-integration: ## Run end-to-end integration test
	@echo "Running integration test..."
	chmod +x deployments/docker-compose/test-integration.sh
	./deployments/docker-compose/test-integration.sh

.PHONY: test-integration-quick
test-integration-quick: dev-env-up ## Quick integration test (assumes services are running)
	@echo "Running quick integration test..."
	@sleep 5
	@export $$(cat configs/local-test.env | grep -v '^#' | xargs) && \
		./build/bin/$(APP_NAME) &
	@SERVICE_PID=$$! && \
		sleep 3 && \
		curl -X POST \
			-H "Content-Type: application/octet-stream" \
			-H "x-rh-identity: $$(echo '{"identity":{"account_number":"12345","org_id":"12345","type":"User"}}' | base64 -w 0)" \
			--data-binary "@deployments/docker-compose/test-data/test-payload.tar.gz" \
			"http://localhost:8080/api/ingress/v1/upload?request_id=test-$$(date +%s)" || true && \
		kill $$SERVICE_PID

.PHONY: test-data
test-data: ## Create test data for integration testing
	@echo "Creating test data..."
	./deployments/docker-compose/create-test-data.sh

.PHONY: verify-kafka
verify-kafka: ## Verify Kafka messages and topics
	@echo "Verifying Kafka setup..."
	./deployments/docker-compose/verify-kafka.sh

.PHONY: verify-minio
verify-minio: ## Verify MinIO uploads and ROS data
	@echo "Verifying MinIO setup..."
	./deployments/docker-compose/verify-minio.sh

.PHONY: monitor-kafka
monitor-kafka: ## Monitor Kafka topics in real-time
	@echo "Monitoring Kafka topics (press Ctrl+C to stop)..."
	./deployments/docker-compose/verify-kafka.sh monitor

.PHONY: install-tools
install-tools: ## Install required development tools
	@echo "Installing development tools..."
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "Installing golangci-lint..."; \
		curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $$(go env GOPATH)/bin latest; \
	}
	@command -v goimports >/dev/null 2>&1 || { \
		echo "Installing goimports..."; \
		go install golang.org/x/tools/cmd/goimports@latest; \
	}
	@command -v mockgen >/dev/null 2>&1 || { \
		echo "Installing mockgen..."; \
		go install github.com/golang/mock/mockgen@latest; \
	}

.PHONY: helm-install
helm-install: ## Install Helm chart from GitHub repository
	@echo "Installing Helm chart from GitHub repository..."
	./deployments/kubernetes/scripts/install-helm-chart.sh

.PHONY: helm-status
helm-status: ## Show Helm deployment status
	@echo "Showing Helm deployment status..."
	./deployments/kubernetes/scripts/install-helm-chart.sh status

.PHONY: helm-cleanup
helm-cleanup: ## Cleanup Helm deployment
	@echo "Cleaning up Helm deployment..."
	./deployments/kubernetes/scripts/install-helm-chart.sh cleanup

.PHONY: security-scan
security-scan: ## Run security scan on container image
	@echo "Running security scan..."
	podman run --rm -v /var/run/docker.sock:/var/run/docker.sock \
		-v $$(pwd):/root/.cache/ \
		aquasec/trivy:latest image $(IMAGE_NAME)

.PHONY: check
check: fmt vet lint test ## Run all checks (format, vet, lint, test)

.PHONY: ci
ci: install-tools check build build-image ## Run CI pipeline

.PHONY: all
all: check build build-image ## Build everything

# Development helpers
.PHONY: watch
watch: ## Watch for changes and rebuild
	@echo "Watching for changes..."
	@command -v entr >/dev/null 2>&1 || { \
		echo "entr is required for watch. Install with: brew install entr"; \
		exit 1; \
	}
	find . -name "*.go" | entr -r make build run

.PHONY: debug
debug: ## Build and run with debugging
	@echo "Building debug version..."
	CGO_ENABLED=1 go build -gcflags="all=-N -l" -o $(BIN_DIR)/$(APP_NAME)-debug ./cmd/$(APP_NAME)
	dlv exec $(BIN_DIR)/$(APP_NAME)-debug

# OpenShift deployment helpers
.PHONY: oc-deploy
oc-deploy: build-image ## Deploy to OpenShift
	@echo "Deploying to OpenShift..."
	@echo "Creating custom values file for image override..."
	@echo "image:" > /tmp/oc-deploy-values.yaml
	@echo "  repository: $(REGISTRY)/$(APP_NAME)" >> /tmp/oc-deploy-values.yaml
	@echo "  tag: $(VERSION)" >> /tmp/oc-deploy-values.yaml
	NAMESPACE=insights-ros VALUES_FILE=/tmp/oc-deploy-values.yaml ./deployments/kubernetes/scripts/install-helm-chart.sh
	@rm -f /tmp/oc-deploy-values.yaml

.PHONY: oc-undeploy
oc-undeploy: ## Remove from OpenShift
	@echo "Removing from OpenShift..."
	NAMESPACE=insights-ros ./deployments/kubernetes/scripts/install-helm-chart.sh cleanup

# Default target
.DEFAULT_GOAL := help