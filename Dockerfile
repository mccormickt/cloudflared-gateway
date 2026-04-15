# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.25 AS builder

WORKDIR /workspace

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/manager ./cmd

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /
COPY --from=builder /out/manager /manager
USER 65532:65532

ENTRYPOINT ["/manager"]
