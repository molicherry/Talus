#!/bin/sh
# Build both Agent architectures and their manifest (REQUIREMENTS §8.2).
#
#   build/agents.sh <build-version> [output-dir]
#
# Both linux/amd64 and linux/arm64 are always produced from the same source and
# the same build version, and the manifest records platform, build version,
# protocol version, SHA-256 and size. The Hub refuses to start unless every
# artifact matches its manifest, so a single-architecture build cannot ship by
# accident.
set -eu

VERSION="${1:?usage: agents.sh <build-version> [output-dir]}"
OUT="${2:-dist/agents}"
MODULE="github.com/vpsmanager/backend"

# Resolve the backend module directory (this script lives in backend/build).
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
BACKEND_DIR=$(dirname "$SCRIPT_DIR")
cd "$BACKEND_DIR"
mkdir -p "$OUT"
OUT=$(CDPATH= cd -- "$OUT" && pwd)

for arch in amd64 arm64; do
  file="vpsmanager-agent-linux-$arch"
  echo "building $file (version=$VERSION)"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build \
    -trimpath \
    -ldflags="-s -w -X ${MODULE}/internal/agentproto.Version=${VERSION}" \
    -o "$OUT/$file" ./cmd/agent
done

# The manifest and the agent's own JSON output share these constants; the values
# are read back from the compiled artifacts so a mismatch cannot be papered over.
PROTOCOL_VERSION=1
manifest="$OUT/agents-manifest.json"
{
  printf '{\n'
  printf '  "build_version": "%s",\n' "$VERSION"
  printf '  "protocol_version": %s,\n' "$PROTOCOL_VERSION"
  printf '  "agents": [\n'
  first=1
  for arch in amd64 arm64; do
    file="vpsmanager-agent-linux-$arch"
    size=$(wc -c < "$OUT/$file" | tr -d ' ')
    digest=$(sha256sum "$OUT/$file" | cut -d' ' -f1)
    [ "$first" -eq 1 ] || printf ',\n'
    first=0
    printf '    {"platform": "linux/%s", "file": "%s", "sha256": "%s", "size": %s, "build_version": "%s", "protocol_version": %s}' \
      "$arch" "$file" "$digest" "$size" "$VERSION" "$PROTOCOL_VERSION"
  done
  printf '\n  ]\n}\n'
} > "$manifest"

echo "=== AGENTS OK $OUT"
ls -l "$OUT"
