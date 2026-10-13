# Talus image — built from the repository root as build context.
# Used by docker compose (docker-compose.yml), CI (ci.yml) and platforms
# that build with the Dockerfile's directory as context.
# backend/Dockerfile is kept in sync for standalone backend-only builds.

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
# Both agent architectures plus their manifest ship in every Hub image: the
# Hub refuses to start unless the manifest matches the artifacts (§8.2).
RUN sh build/agents.sh "${VERSION}" /agents
# Verify the artifacts this image will ship (TC-08-05): manifest, both agent
# architectures, ELF class/machine, size, hash and the stamped build version. The
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

FROM alpine:3.20
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
# The probe is verified by value, not by substring: "protocol_version": 10 must not
# satisfy a check for 1, and the reported agent version must equal the version this
# image was built with (N07). Only POSIX tools are used - the image has no jq and no
# python - so the check is an exact-field awk pass.
ARG VERSION=dev
RUN /usr/local/bin/vpsmanager-agent-linux-${TARGETARCH} --format json > /tmp/agent-probe.json \
 && awk -v want="${VERSION}" '\
      function take(pattern,   s) { if (match($0, pattern)) { return substr($0, RSTART, RLENGTH) } return "" }\
      {\
        v = take("\"protocol_version\"[ \t]*:[ \t]*[0-9]+");\
        if (v != "") { sub(/.*:[ \t]*/, "", v); if (v == "1") proto = 1 }\
        ver = take("\"agent_version\"[ \t]*:[ \t]*\"[^\"]*\"");\
        if (ver != "") { sub(/.*:[ \t]*"/, "", ver); sub(/"$/, "", ver); if (ver == want) version = 1 }\
      }\
      END { exit (proto && version) ? 0 : 1 }\
    ' /tmp/agent-probe.json \
 && rm -f /tmp/agent-probe.json
EXPOSE 8080
ENV STATIC_DIR=/static
USER talus
ENTRYPOINT ["hub"]
