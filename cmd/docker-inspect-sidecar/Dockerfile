# Multi-stage build: build the static binary in golang:alpine, run it from
# distroless/static. Total runtime image size: ~10 MB.

# ---------- build ----------
FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/docker-inspect-sidecar \
    ./cmd/docker-inspect-sidecar

# ---------- runtime ----------
FROM gcr.io/distroless/static:nonroot

# nonroot user is uid/gid 65532. The container running this image must add the
# host docker socket's GID to that user via docker-compose's group_add: directive
# (NOT in the Dockerfile, which has no knowledge of the host's docker GID).
USER nonroot:nonroot

COPY --from=build /out/docker-inspect-sidecar /docker-inspect-sidecar

EXPOSE 8081
ENV LISTEN_ADDR=":8081"

ENTRYPOINT ["/docker-inspect-sidecar"]
