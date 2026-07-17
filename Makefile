.PHONY: build clean run test build-image build-imagex86 build-imagearm64 help

BINARY_NAME=docker-mcp
BUILD_DIR=./bin
IMAGE_NAME=docker-mcp
REGISTRY=ghcr.io/ttb267
VERSION=latest

build:
	@echo "Building Docker MCP Server..."
	@mkdir -p $(BUILD_DIR)
	@go build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/server

clean:
	@echo "Cleaning..."
	@rm -rf $(BUILD_DIR)
	@go clean

run: build
	@echo "Running Docker MCP Server..."
	@$(BUILD_DIR)/$(BINARY_NAME)

test:
	@echo "Running tests..."
	@go test -v ./...

deps:
	@echo "Installing dependencies..."
	@go mod download
	@go mod tidy

lint:
	@echo "Running linter..."
	@go vet ./...

fmt:
	@echo "Formatting code..."
	@gofmt -w ./cmd ./internal ./pkg

# 构建本地镜像
build-image:
	@echo "Building Docker image: $(IMAGE_NAME):$(VERSION)"
	@docker build -t $(IMAGE_NAME):$(VERSION) .
	@echo "Image built successfully!"

# 构建多平台镜像 (x86_64 + arm64)
build-imagex86:
	@echo "Building Docker image for x86_64: $(IMAGE_NAME):$(VERSION)-x86"
	@docker build --platform linux/amd64 -t $(IMAGE_NAME):$(VERSION)-x86 .
	@echo "Image built successfully!"

build-imagearm64:
	@echo "Building Docker image for arm64: $(IMAGE_NAME):$(VERSION)-arm64"
	@docker build --platform linux/arm64 -t $(IMAGE_NAME):$(VERSION)-arm64 .
	@echo "Image built successfully!"

# 构建并推送到 Registry
push:
	@echo "Building and pushing image to registry..."
	@docker build -t $(REGISTRY)/$(IMAGE_NAME):$(VERSION) .
	@docker push $(REGISTRY)/$(IMAGE_NAME):$(VERSION)

help:
	@echo "Available commands:"
	@echo "  make build         - Build the binary"
	@echo "  make run           - Build and run the server"
	@echo "  make clean         - Clean build artifacts"
	@echo "  make test          - Run tests"
	@echo "  make deps          - Install dependencies"
	@echo "  make lint          - Run linter"
	@echo "  make fmt           - Format code"
	@echo "  make build-image   - Build Docker image (local)"
	@echo "  make push          - Build and push to registry"