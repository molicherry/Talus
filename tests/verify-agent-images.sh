#!/usr/bin/env bash
# Four image/platform combinations, one acceptance check (TC-08-05).
#
# Each Dockerfile's artifact stage verifies what the image will ship: the
# manifest, both agent architectures (ELF class/machine, size, SHA-256) and the
# stamped build version, and the architecture the builder can execute also runs
# the agent's protocol smoke test. The check runs inside the build, so a mismatch
# fails the build itself instead of being reported after the fact.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${AGENT_IMAGE_VERSION:-ci-agent-check}"
GOPROXY_ARG="${AGENT_IMAGE_GOPROXY:-https://goproxy.cn,direct}"
NPM_REGISTRY_ARG="${AGENT_IMAGE_NPM_REGISTRY:-https://registry.npmmirror.com}"
BUILD_TIMEOUT="${AGENT_IMAGE_BUILD_TIMEOUT:-900}"
LAB="$(mktemp -d)"
trap 'rm -rf "$LAB"' EXIT
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

fail() {
	echo "✗ $*" >&2
	exit 1
}

for dockerfile in Dockerfile backend/Dockerfile; do
	for arch in amd64 arm64; do
		label="$(dirname "$dockerfile") linux/$arch"
		echo "▶ $label (artifact stage)"
		if ! timeout "$BUILD_TIMEOUT" docker build --target go-builder --platform "linux/$arch" \
			--build-arg VERSION="$VERSION" \
			--build-arg GOPROXY="$GOPROXY_ARG" \
			--build-arg NPM_REGISTRY="$NPM_REGISTRY_ARG" \
			-f "$dockerfile" . >/dev/null; then
			fail "$label: the image's agent artifacts did not verify"
		fi
		# The final stage is where the *image* is accepted: it executes the
		# TARGETARCH agent and compares the protocol and the build version by value.
		# Building only the artifact stage would leave that check unexercised, so
		# every combination builds the final image too and re-reads the manifest
		# from inside it (arm64 runs under QEMU here).
		echo "▶ $label (final stage)"
		if ! timeout "$BUILD_TIMEOUT" docker build --platform "linux/$arch" \
			--build-arg VERSION="$VERSION" \
			--build-arg GOPROXY="$GOPROXY_ARG" \
			--build-arg NPM_REGISTRY="$NPM_REGISTRY_ARG" \
			-f "$dockerfile" -t "talus-image-check:${arch}-$(basename "$(dirname "$dockerfile")")" . >/dev/null; then
			fail "$label: the final image did not accept its own agent"
		fi
		# The build already accepted the image with the strict rule. This re-read
		# proves the *published* image answers correctly, and it applies the same
		# strict decoder instead of the prefix matching it replaced (P2-04): the
		# report is decoded by type, so a fractional or exponent protocol cannot pass.
		image="talus-image-check:${arch}-$(basename "$(dirname "$dockerfile")")"
		if ! docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$image" \
			-c '/usr/local/bin/vpsmanager-agent-linux-'"$arch"' --format json' \
			> "$LAB/probe.json" 2>/dev/null; then
			fail "$label: the shipped agent did not run"
		fi
		if ! docker run --rm --platform "linux/$arch" --entrypoint /bin/sh "$image" \
			-c 'cat /usr/local/bin/agents-manifest.json' > "$LAB/agents-manifest.json" 2>/dev/null; then
			fail "$label: the image does not ship the agent manifest"
		fi
		if ! ( cd "$ROOT/backend" && go build -o "$LAB/agentverify" ./cmd/agentverify ); then
			fail "$label: could not build the report verifier"
		fi
		if ! "$LAB/agentverify" -check-json "$LAB/probe.json" "$LAB" >/dev/null; then
			fail "$label: the shipped agent report failed the strict protocol check"
		fi
	done
done

echo "✓ agent artifacts and the final images' own protocol check verified for both Dockerfiles on amd64 and arm64"
