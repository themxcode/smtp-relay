# =============================================================================
# ContaCloud SMTP-to-SendGrid Relay
# Multi-stage build for minimal image size (~15MB)
# =============================================================================

# Stage 1: Build
FROM golang:1.22-alpine AS builder

# Install git for go mod download (some deps might need it)
RUN apk add --no-cache git ca-certificates

WORKDIR /app

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY main.go ./

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags="-w -s" \
    -a -installsuffix cgo \
    -o smtp-relay .

# Stage 2: Runtime
FROM alpine:3.19

# Install CA certificates for HTTPS calls to SendGrid
RUN apk --no-cache add ca-certificates tzdata

# Create non-root user
RUN adduser -D -g '' appuser

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/smtp-relay .

# Use non-root user
USER appuser

# Expose SMTP port
EXPOSE 25

# Health check - verify SMTP port is listening
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD nc -z localhost 25 || exit 1

# Default environment variables
ENV SMTP_LISTEN_ADDR=":25" \
    SMTP_DOMAIN="localhost" \
    LOG_LEVEL="info"

# Run the relay
CMD ["./smtp-relay"]
