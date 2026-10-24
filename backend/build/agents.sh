#!/bin/sh
# Build all supported Agent platforms and their versioned artifact manifest.
#
#   build/agents.sh <build-version> [output-dir]
#
# Linux amd64/arm64 and Windows amd64 are produced from the same source and
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

for platform in linux/amd64 linux/arm64 windows/amd64; do
	os=${platform%/*}
	arch=${platform#*/}
	file="vpsmanager-agent-$os-$arch"
	[ "$os" != windows ] || file="$file.exe"
	echo "building $file (version=$VERSION)"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
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
	for platform in linux/amd64 linux/arm64 windows/amd64; do
		os=${platform%/*}
		arch=${platform#*/}
		file="vpsmanager-agent-$os-$arch"
		[ "$os" != windows ] || file="$file.exe"
		size=$(wc -c <"$OUT/$file" | tr -d ' ')
		digest=$(sha256sum "$OUT/$file" | cut -d' ' -f1)
		[ "$first" -eq 1 ] || printf ',\n'
		first=0
		printf '    {"platform": "%s", "file": "%s", "sha256": "%s", "size": %s, "build_version": "%s", "protocol_version": %s}' \
			"$platform" "$file" "$digest" "$size" "$VERSION" "$PROTOCOL_VERSION"
	done
	printf '\n  ]\n}\n'
} >"$manifest"

echo "=== AGENTS OK $OUT"
ls -l "$OUT"
