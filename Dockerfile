# Build stage
FROM golang:1.21-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Set working directory
WORKDIR /app

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build information
ARG VERSION=dev
ARG GIT_COMMIT=unknown
ARG BUILD_DATE=unknown

# Build binaries
RUN for cmd in loopworker loopctl loopdebug loopwatch loopbench loopsim; do \
    CGO_ENABLED=1 GOOS=linux go build \
    -ldflags "-X loopworker/version.Version=${VERSION} \
              -X loopworker/version.GitCommit=${GIT_COMMIT} \
              -X loopworker/version.BuildDate=${BUILD_DATE}" \
    -o /app/bin/${cmd} ./cmd/${cmd}/; \
    done

# Runtime stage
FROM alpine:3.19

# Install runtime dependencies
RUN apk add --no-cache ca-certificates tzdata

# Create non-root user
RUN addgroup -S loopworker && adduser -S loopworker -G loopworker

# Set working directory
WORKDIR /app

# Copy binaries from builder
COPY --from=builder /app/bin/ /usr/local/bin/

# Create data directory
RUN mkdir -p /data/plugins /data/db && chown -R loopworker:loopworker /data

# Set user
USER loopworker

# Expose port
EXPOSE 19527

# Default command
CMD ["loopworker"]
