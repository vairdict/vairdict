#!/bin/sh
# install.sh — download and install the latest vairdict binary.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/vairdict/vairdict/main/scripts/install.sh | sh
#
# Respects:
#   INSTALL_DIR  — where to put the binary (default: /usr/local/bin)
#   VERSION      — specific version to install (default: latest)
#   GITHUB_TOKEN — bearer token for the releases API call. When set,
#                  the latest-version probe is authenticated, raising
#                  the rate limit from 60/hour (anon) to 5000/hour
#                  and avoiding the "Failed to detect latest version"
#                  exit that hits CI runner pools sharing IP space.

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
  # fetch_latest queries the releases/latest API. When GITHUB_TOKEN is
  # set, sends Authorization so the request is authenticated (5000/hour
  # rate limit) instead of anonymous (60/hour shared by IP across the
  # runner pool — easy to exhaust on busy days). Two branches keep the
  # quoting clean: building one with a header injected via shell
  # variable would word-split badly.
  fetch_latest() {
    if [ -n "$GITHUB_TOKEN" ]; then
      curl -fsSL \
        -H "Authorization: Bearer ${GITHUB_TOKEN}" \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: vairdict-install.sh" \
        "https://api.github.com/repos/${REPO}/releases/latest"
    else
      curl -fsSL \
        -H "Accept: application/vnd.github+json" \
        -H "User-Agent: vairdict-install.sh" \
        "https://api.github.com/repos/${REPO}/releases/latest"
    fi
  }

  # Retry on transient API failures (rate-limit blips, network jitter,
  # transient 5xx). Without retry the latest-version probe is a single
  # point of failure on every PR's review check.
  attempt=1
  while [ "$attempt" -le 3 ]; do
    if VERSION="$(fetch_latest | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')" && [ -n "$VERSION" ]; then
      break
    fi
    echo "Attempt ${attempt}: failed to fetch latest version, retrying..." >&2
    attempt=$((attempt + 1))
    sleep 2
  done
  if [ -z "$VERSION" ]; then
    echo "Failed to detect latest version after 3 attempts. Set VERSION explicitly." >&2
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
