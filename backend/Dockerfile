FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-builder
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w -X github.com/vpsmanager/backend/internal/server.Version=${VERSION}" -o /hub ./cmd/server
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o /agent ./cmd/agent

FROM --platform=$BUILDPLATFORM node:22-alpine AS ui-builder
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
