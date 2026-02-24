# Stage 1: Build the Go binaries
FROM golang:1.26-alpine AS builder

RUN apk add --no-cache git make

WORKDIR /src

# Copy dependency files first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

# Copy the full source tree.
COPY . .

# Build both binaries with static linking for alpine runtime.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/novaroute-agent ./cmd/novaroute-agent/
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/novaroutectl ./cmd/novaroutectl/

# Stage 2: Minimal runtime image
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata \
    && apk add --no-cache \
       --repository https://dl-cdn.alpinelinux.org/alpine/v3.21/main \
       --repository https://dl-cdn.alpinelinux.org/alpine/v3.21/community \
       frr

# Create the default socket directory.
RUN mkdir -p /run/novaroute /etc/novaroute

# Copy the built binaries from the builder stage.
COPY --from=builder /out/novaroute-agent /usr/local/bin/novaroute-agent
COPY --from=builder /out/novaroutectl /usr/local/bin/novaroutectl

# Default config path (overridden by volume mount in Kubernetes).
VOLUME ["/etc/novaroute"]

# Prometheus metrics port.
EXPOSE 9102

ENTRYPOINT ["novaroute-agent"]
CMD ["--config=/etc/novaroute/config.json"]
