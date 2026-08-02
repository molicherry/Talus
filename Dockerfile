# Talus image — built from the repository root as build context.
# Used by docker compose (docker-compose.yml), CI (ci.yml) and platforms
# that build with the Dockerfile's directory as context.
# backend/Dockerfile is kept in sync for standalone backend-only builds.

FROM golang:1.25-alpine AS go-builder
ARG VERSION=dev
ARG TARGETARCH=amd64
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X github.com/vpsmanager/backend/internal/server.Version=${VERSION}" -o /hub ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /agent ./cmd/agent

FROM node:22-alpine AS ui-builder
ARG VERSION=dev
ARG NPM_REGISTRY=https://registry.npmjs.org
ENV VITE_APP_VERSION=${VERSION}
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm config set registry ${NPM_REGISTRY} && npm ci
COPY frontend/ .
RUN npm run build

FROM alpine:3.20
RUN apk add --no-cache openssh-client ca-certificates tzdata
RUN adduser -D -H -s /sbin/nologin talus
COPY --from=go-builder /hub /usr/local/bin/hub
COPY --from=go-builder /agent /usr/local/bin/vpsmanager-agent
COPY --from=ui-builder /app/dist/ /static/
EXPOSE 8080
ENV STATIC_DIR=/static
USER talus
ENTRYPOINT ["hub"]
