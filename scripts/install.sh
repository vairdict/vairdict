#!/bin/sh
# install.sh — download and install the latest vairdict binary.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/vairdict/vairdict/main/scripts/install.sh | sh
#
# Respects:
#   INSTALL_DIR  — where to put the binary (default: /usr/local/bin)
#   VERSION      — specific version to install (default: latest)
#
# Version resolution: this script follows the github.com /releases/latest
# 302 redirect rather than calling the api.github.com REST endpoint.
# The web redirect is not auth-rate-limited (the API path is — 60/hour
# per anonymous IP, easily exhausted by CI runner pools sharing IPs,
# which produced the recurring exit-22 failures the auto-review action
# was hitting). No GITHUB_TOKEN needed.

set -e
# pipefail makes a curl failure inside a `curl | grep | sed` pipeline
# propagate to set -e instead of producing empty output that the
# downstream tools cheerfully consume. Posix /bin/sh doesn't all
# support `set -o pipefail`; guard so this still runs on dash etc.
(set -o pipefail) 2>/dev/null && set -o pipefail || true

REPO="vairdict/vairdict"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# --- Detect OS and arch ---

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
  arm64)   ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

case "$OS" in
  darwin|linux) ;;
  *)
    echo "Unsupported OS: $OS" >&2
    exit 1
    ;;
esac

# --- Resolve version ---

if [ -z "$VERSION" ]; then
  # fetch_latest follows the github.com /releases/latest redirect and
  # returns the version from the Location header (e.g.
  # `https://github.com/.../releases/tag/v0.0.8` -> `0.0.8`). Curl's
  # `-I` issues a HEAD request so the body is never downloaded; `-f`
  # makes 4xx/5xx return non-zero so this falls into the retry loop
  # instead of producing empty output downstream.
  fetch_latest() {
    curl -fsSI \
      -H "User-Agent: vairdict-install.sh" \
      "https://github.com/${REPO}/releases/latest" \
      | awk 'tolower($1) == "location:" {
                sub(/\r$/, "", $2)
                n = split($2, a, "/")
                v = a[n]
                sub(/^v/, "", v)
                print v
                exit
              }'
  }

  # Retry on transient network blips. The redirect endpoint is the
  # same path the tarball download will hit moments later, so a
  # persistent failure here is a strong signal the rest will fail
  # too — bail with a clear message rather than retrying forever.
  attempt=1
  while [ "$attempt" -le 3 ]; do
    if VERSION="$(fetch_latest)" && [ -n "$VERSION" ]; then
      break
    fi
    echo "Attempt ${attempt}: failed to resolve latest version, retrying..." >&2
    attempt=$((attempt + 1))
    sleep 2
  done
  if [ -z "$VERSION" ]; then
    echo "Failed to resolve latest version after 3 attempts. Set VERSION explicitly." >&2
    exit 1
  fi
fi

# Strip leading 'v' if present.
VERSION="${VERSION#v}"

# --- Download and install ---

TARBALL="vairdict_${VERSION}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/v${VERSION}/${TARBALL}"

echo "Downloading vairdict v${VERSION} for ${OS}/${ARCH}..."

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

curl -fsSL "$URL" -o "${TMPDIR}/${TARBALL}"

# Verify checksum.
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/v${VERSION}/checksums.txt"
curl -fsSL "$CHECKSUMS_URL" -o "${TMPDIR}/checksums.txt"
EXPECTED="$(grep "${TARBALL}" "${TMPDIR}/checksums.txt" | awk '{print $1}')"
if [ -z "$EXPECTED" ]; then
  echo "Warning: no checksum found for ${TARBALL}, skipping verification" >&2
else
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL="$(sha256sum "${TMPDIR}/${TARBALL}" | awk '{print $1}')"
  else
    ACTUAL="$(shasum -a 256 "${TMPDIR}/${TARBALL}" | awk '{print $1}')"
  fi
  if [ "$EXPECTED" != "$ACTUAL" ]; then
    echo "Checksum mismatch! Expected ${EXPECTED}, got ${ACTUAL}" >&2
    exit 1
  fi
  echo "Checksum verified."
fi

tar -xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"

# Install binary.
if [ -w "$INSTALL_DIR" ]; then
  cp "${TMPDIR}/vairdict" "${INSTALL_DIR}/vairdict"
else
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo cp "${TMPDIR}/vairdict" "${INSTALL_DIR}/vairdict"
fi

chmod +x "${INSTALL_DIR}/vairdict"

echo "vairdict v${VERSION} installed to ${INSTALL_DIR}/vairdict"
