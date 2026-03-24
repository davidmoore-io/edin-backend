FROM golang:1.24-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

RUN go mod tidy

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/control-api ./cmd/control-api

FROM debian:bookworm-slim AS runtime

ENV DEBIAN_FRONTEND=noninteractive

ARG TARGETARCH=amd64
ARG DOCKER_CLI_VERSION=27.3.1

RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends \
    ca-certificates \
    chromium \
    chromium-sandbox \
    curl \
    fonts-liberation \
    libasound2 \
    libatk-bridge2.0-0 \
    libatk1.0-0 \
    libcups2 \
    libdbus-1-3 \
    libdrm2 \
    libgbm1 \
    libgtk-3-0 \
    libnspr4 \
    libnss3 \
    libxcomposite1 \
    libxdamage1 \
    libxfixes3 \
    libxkbcommon0 \
    libxrandr2 \
    xdg-utils; \
    rm -rf /var/lib/apt/lists/*; \
    case "${TARGETARCH}" in \
        amd64) docker_arch='x86_64' ;; \
        arm64) docker_arch='aarch64' ;; \
        *) echo "unsupported TARGETARCH: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://download.docker.com/linux/static/stable/${docker_arch}/docker-${DOCKER_CLI_VERSION}.tgz" -o /tmp/docker.tgz; \
    tar -xzf /tmp/docker.tgz -C /tmp; \
    mv /tmp/docker/docker /usr/local/bin/; \
    rm -rf /tmp/docker /tmp/docker.tgz; \
    update-ca-certificates; \
    docker --version; \
    chromium --version

ENV CHROME_PATH=/usr/bin/chromium
ENV CHROMEDP_NO_SANDBOX=true

COPY --from=builder /out/control-api /usr/local/bin/control-api

WORKDIR /srv/app

EXPOSE 8080 8081

ENTRYPOINT ["control-api"]
