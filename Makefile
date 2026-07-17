.PHONY: build clean run test

BINARY_NAME=docker-mcp
BUILD_DIR=./bin

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

help:
	@echo "Available commands:"
	@echo "  make build    - Build the binary"
	@echo "  make run      - Build and run the server"
	@echo "  make clean    - Clean build artifacts"
	@echo "  make test     - Run tests"
	@echo "  make deps     - Install dependencies"
	@echo "  make lint     - Run linter"
	@echo "  make fmt      - Format code"
	@echo "  make help     - Show this help message"