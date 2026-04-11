#!/bin/sh
# install.sh — download and install the latest vairdict binary.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/vairdict/vairdict/main/scripts/install.sh | sh
#
# Respects:
#   INSTALL_DIR  — where to put the binary (default: /usr/local/bin)
#   VERSION      — specific version to install (default: latest)

set -e

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
  VERSION="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"v([^"]+)".*/\1/')"
  if [ -z "$VERSION" ]; then
    echo "Failed to detect latest version. Set VERSION explicitly." >&2
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
