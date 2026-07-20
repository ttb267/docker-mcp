# Build stage
FROM --platform=${BUILDPLATFORM:-linux/amd64} swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/golang:1.25.5-alpine3.22 AS builder

# Install build dependencies
RUN apk add --no-cache git make

# Set Go module proxy for China
RUN go env -w GOPROXY=https://goproxy.cn,direct

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o docker-mcp ./cmd/server

# Production stage
FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/alpine:3.22

# Install ca-certificates for HTTPS and docker-cli for docker socket access
RUN apk add --no-cache ca-certificates docker-cli

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/docker-mcp .

# Add to PATH
RUN chmod +x docker-mcp && mv docker-mcp /usr/local/bin/

# Expose HTTP port for remote agent
EXPOSE 8080

# Create non-root user for security
RUN adduser -D -u 1000 appuser
USER appuser

# Default to HTTP mode for remote access
CMD ["--mode", "http", "--port", "8080"]