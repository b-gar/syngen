#########################################################################
# Builder: Build Golang binary
#########################################################################
FROM golang:1.27.2-alpine AS builder

WORKDIR /app

# Copy module files and download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy application source code
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY .golangci.yml ./

# Build a statically linked binary for minimal footprint
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o syngen ./cmd/syngen

# Pre-create runtime-writable directories for the final nonroot image
RUN mkdir -p /app/output

#########################################################################
# Test: Install linter for CI tests
#########################################################################
FROM builder AS test

RUN go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

#########################################################################
# Release: Distroless base image copying only what is needed from builder
#########################################################################
FROM cgr.dev/chainguard/static:latest AS release

WORKDIR /app

# Copy the static binary from builder
COPY --from=builder /app/syngen /app/syngen

# Copy schemas and seeds
COPY schemas ./schemas
COPY seeds ./seeds

# Copy pre-created writable directories with nonroot ownership

COPY --chown=nonroot:nonroot --from=builder /app/output /app/output

USER nonroot:nonroot
ENTRYPOINT ["/app/syngen"]
