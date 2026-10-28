FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS go-builder
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
# Both agent architectures plus their manifest ship in every Hub image: the
# Hub refuses to start unless the manifest matches the bundled artifacts.
RUN sh build/agents.sh "${VERSION}" /agents
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -o /agentverify ./cmd/agentverify
# Verify the artifacts this image will ship (TC-08-05): manifest, all agent
# platforms, ELF/PE class/machine, size, hash and the stamped build version. The
# architecture this builder can execute additionally runs the protocol smoke test,
# so every image/platform combination fails closed on a mismatch.
RUN go run ./cmd/agentverify -version-in-binary /agents && \
    if [ "$TARGETARCH" = "$(go env GOHOSTARCH)" ]; then go run ./cmd/agentverify -run-protocol /agents; fi

FROM --platform=$BUILDPLATFORM node:24-alpine AS ui-builder
ARG VERSION=dev
ARG NPM_REGISTRY=https://registry.npmjs.org
ENV VITE_APP_VERSION=${VERSION}
WORKDIR /app
COPY frontend/package.json frontend/package-lock.json ./
RUN npm config set registry ${NPM_REGISTRY} && npm ci
COPY frontend/ .
RUN npm run build

FROM alpine:3.24
RUN apk add --no-cache openssh-client ca-certificates tzdata
RUN adduser -D -H -s /sbin/nologin talus
COPY --from=go-builder /hub /usr/local/bin/hub
COPY --from=go-builder /agents/ /usr/local/bin/
COPY --from=ui-builder /app/dist/ /static/
# The final image proves it can run the agent for its own architecture: the
# protocol smoke test executes the TARGETARCH binary (arm64 included, under
# emulation in CI). Verification inside the build means a broken image cannot be
# published (TC-08-05).
ARG TARGETARCH
# The probe is verified with the same strict JSON rule the post-build check uses:
# the report is decoded by type, so a fractional or exponent protocol (1.5, 1e9), a
# string protocol or a non-matching version is rejected instead of being matched as
# a prefix (N07, P2-04). The verifier is copied in for the check and removed again,
# so the published image carries no extra tool.
ARG VERSION=dev
COPY --from=go-builder /agentverify /usr/local/bin/agentverify
RUN /usr/local/bin/vpsmanager-agent-linux-${TARGETARCH} --format json > /tmp/agent-probe.json \
 && /usr/local/bin/agentverify -check-json /tmp/agent-probe.json /usr/local/bin \
 && rm -f /tmp/agent-probe.json /usr/local/bin/agentverify
EXPOSE 8080
ENV STATIC_DIR=/static
USER talus
ENTRYPOINT ["hub"]
