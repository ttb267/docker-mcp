# Build stage
FROM --platform=${BUILDPLATFORM:-linux/amd64} swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/golang:1.25.3-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git make

WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o docker-mcp ./cmd/server

# Production stage
FROM swr.cn-north-4.myhuaweicloud.com/ddn-k8s/docker.io/library/alpine:3.19

# Install ca-certificates for HTTPS and docker-cli for docker socket access
RUN apk add --no-cache ca-certificates docker-cli

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/docker-mcp .

# Expose HTTP port for remote agent
EXPOSE 8080

# Create non-root user for security
RUN adduser -D -u 1000 appuser
USER appuser

# Default to HTTP mode for remote access
CMD ["--mode", "http", "--port", "8080"]