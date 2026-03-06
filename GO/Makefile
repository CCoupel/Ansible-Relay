.PHONY: build build-linux docker-build docker-push test clean help

BUILD_DIR := ./build
BINARY_NAME := relay-server
DOCKER_IMAGE := relay-server:latest
DOCKER_REGISTRY ?= docker.io

# Build for current platform
build:
	@echo "Building $(BINARY_NAME) for $(shell go env GOOS)/$(shell go env GOARCH)..."
	@mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server
	@echo "✓ Built to $(BUILD_DIR)/$(BINARY_NAME)"

# Build for Linux x86_64
build-linux:
	@echo "Cross-compiling $(BINARY_NAME) for Linux/x86_64..."
	@mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY_NAME)-linux ./cmd/server
	@echo "✓ Built to $(BUILD_DIR)/$(BINARY_NAME)-linux"

# Build optimized binary (strip symbols)
build-optimized:
	@echo "Building optimized $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	go build -ldflags="-s -w" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server
	@echo "✓ Built optimized binary to $(BUILD_DIR)/$(BINARY_NAME)"

# Docker build
docker-build: build-linux
	@echo "Building Docker image $(DOCKER_IMAGE)..."
	docker build -t $(DOCKER_IMAGE) .
	@echo "✓ Docker image built: $(DOCKER_IMAGE)"

# Docker push
docker-push: docker-build
	@echo "Pushing $(DOCKER_IMAGE) to $(DOCKER_REGISTRY)..."
	docker push $(DOCKER_REGISTRY)/$(DOCKER_IMAGE)
	@echo "✓ Pushed to $(DOCKER_REGISTRY)/$(DOCKER_IMAGE)"

# Run tests
test:
	@echo "Running tests..."
	go test -v -cover ./...

# Run tests with coverage report
test-coverage:
	@echo "Running tests with coverage..."
	go test -v -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "✓ Coverage report: coverage.html"

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	rm -rf $(BUILD_DIR)
	rm -f coverage.out coverage.html
	@echo "✓ Cleaned"

# Download dependencies
deps:
	@echo "Downloading dependencies..."
	go mod download
	go mod verify
	@echo "✓ Dependencies verified"

# Format code
fmt:
	@echo "Formatting code..."
	go fmt ./...
	@echo "✓ Formatted"

# Lint code
lint:
	@echo "Linting code..."
	go vet ./...
	@echo "✓ Linted"

# Help
help:
	@echo "Relay Server Makefile"
	@echo ""
	@echo "Targets:"
	@echo "  build              - Build for current platform"
	@echo "  build-linux        - Cross-compile for Linux x86_64"
	@echo "  build-optimized    - Build optimized binary (strip symbols)"
	@echo "  docker-build       - Build Docker image"
	@echo "  docker-push        - Build and push Docker image"
	@echo "  test               - Run unit tests"
	@echo "  test-coverage      - Run tests with coverage report"
	@echo "  clean              - Clean build artifacts"
	@echo "  deps               - Download and verify dependencies"
	@echo "  fmt                - Format code"
	@echo "  lint               - Lint code"
	@echo "  help               - Show this help"
