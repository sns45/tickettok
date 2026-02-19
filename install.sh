#!/bin/sh
set -e

REPO="sns45/tickettok"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"

# Detect OS
OS="$(uname -s)"
case "$OS" in
  Linux*)  OS="linux" ;;
  Darwin*) OS="darwin" ;;
  *)
    echo "Unsupported OS: $OS (tickettok requires tmux — macOS or Linux only)" >&2
    exit 1
    ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH" >&2
    exit 1
    ;;
esac

# Fetch latest release tag
TAG="$(curl -sSf "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | cut -d'"' -f4)"
if [ -z "$TAG" ]; then
  echo "Failed to fetch latest release tag" >&2
  exit 1
fi

TARBALL="tickettok_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${TARBALL}"

echo "Downloading tickettok ${TAG} for ${OS}/${ARCH}..."

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

curl -sSfL "$URL" -o "${TMPDIR}/${TARBALL}"
tar -xzf "${TMPDIR}/${TARBALL}" -C "$TMPDIR"

if [ ! -w "$INSTALL_DIR" ]; then
  echo "Installing to ${INSTALL_DIR} (requires sudo)..."
  sudo install -m 755 "${TMPDIR}/tickettok" "${INSTALL_DIR}/tickettok"
else
  install -m 755 "${TMPDIR}/tickettok" "${INSTALL_DIR}/tickettok"
fi

echo "tickettok ${TAG} installed to ${INSTALL_DIR}/tickettok"
