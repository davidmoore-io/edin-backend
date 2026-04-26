# Multi-stage build matching cmd/docker-inspect-sidecar/Dockerfile's pattern.

# ---------- build ----------
FROM golang:1.24-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/edin-bot \
    ./cmd/edin-bot

# ---------- runtime ----------
FROM gcr.io/distroless/static:nonroot

USER nonroot:nonroot
COPY --from=build /out/edin-bot /edin-bot

EXPOSE 8080
ENV BINDINGS_PATH=/etc/edin-bot/bindings.yml

ENTRYPOINT ["/edin-bot"]
