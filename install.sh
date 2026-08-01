#!/usr/bin/env sh
set -eu

REPO="joshuadavidthomas/ts-skills"
INSTALL_DIR="${TS_SKILLS_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${TS_SKILLS_VERSION:-latest}"
REQUIRE_ATTESTATION="${TS_SKILLS_REQUIRE_ATTESTATION:-0}"

download() {
  url="$1"
  destination="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$destination"
    return
  fi
  if command -v wget >/dev/null 2>&1; then
    wget -qO "$destination" "$url"
    return
  fi
  echo "error: install requires curl or wget" >&2
  exit 1
}

sha256_file() {
  file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
    return
  fi
  echo "error: install requires sha256sum or shasum" >&2
  exit 1
}

# Exit codes: 0 = verified, 1 = failed, 2 = verification unavailable.
verify_provenance() {
  digest="$1"
  if ! command -v gh >/dev/null 2>&1; then
    return 2
  fi
  if ! gh auth status >/dev/null 2>&1; then
    return 2
  fi
  if ! gh api "repos/${REPO}/attestations/sha256:${digest}" >/dev/null 2>&1; then
    return 2
  fi
  if gh attestation verify "$ARCHIVE_PATH" --repo "$REPO"; then
    return 0
  fi
  return 1
}

normalize_os() {
  case "$(uname -s)" in
    Linux) echo "linux" ;;
    Darwin) echo "darwin" ;;
    *)
      echo "error: unsupported OS $(uname -s)" >&2
      exit 1
      ;;
  esac
}

normalize_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    arm64|aarch64) echo "arm64" ;;
    *)
      echo "error: unsupported architecture $(uname -m)" >&2
      exit 1
      ;;
  esac
}

for command in tar awk mktemp; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "error: required command not found: $command" >&2
    exit 1
  fi
done

OS="$(normalize_os)"
ARCH="$(normalize_arch)"
ASSET="ts-skills_${OS}_${ARCH}.tar.gz"
CHECKSUMS="checksums.txt"

if [ "$VERSION" = "latest" ]; then
  DOWNLOAD_URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
  CHECKSUMS_URL="https://github.com/${REPO}/releases/latest/download/${CHECKSUMS}"
else
  case "$VERSION" in
    v*) TAG="$VERSION" ;;
    *) TAG="v$VERSION" ;;
  esac
  DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"
  CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${TAG}/${CHECKSUMS}"
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

ARCHIVE_PATH="$TMP_DIR/$ASSET"
CHECKSUMS_PATH="$TMP_DIR/$CHECKSUMS"

echo "Downloading ${ASSET}..."
download "$DOWNLOAD_URL" "$ARCHIVE_PATH"
download "$CHECKSUMS_URL" "$CHECKSUMS_PATH"

EXPECTED_SUM="$(awk -v name="$ASSET" '$2 == name || $2 == "*"name { print $1; exit }' "$CHECKSUMS_PATH")"
if [ -z "$EXPECTED_SUM" ]; then
  echo "error: could not find checksum entry for ${ASSET}" >&2
  exit 1
fi

ACTUAL_SUM="$(sha256_file "$ARCHIVE_PATH")"
if [ "$EXPECTED_SUM" != "$ACTUAL_SUM" ]; then
  echo "error: checksum mismatch for ${ASSET}" >&2
  echo "expected: $EXPECTED_SUM" >&2
  echo "actual:   $ACTUAL_SUM" >&2
  exit 1
fi

ATTESTATION_STATUS=0
verify_provenance "$ACTUAL_SUM" || ATTESTATION_STATUS=$?
case "$ATTESTATION_STATUS" in
  0)
    echo "Build provenance verified."
    ;;
  1)
    echo "error: build provenance verification failed for ${ASSET}" >&2
    exit 1
    ;;
  2)
    if [ "$REQUIRE_ATTESTATION" = "1" ]; then
      echo "error: TS_SKILLS_REQUIRE_ATTESTATION=1 is set, but provenance verification is unavailable" >&2
      exit 1
    fi
    echo "note: skipping build-provenance verification"
    ;;
esac

mkdir -p "$INSTALL_DIR"
tar -xzf "$ARCHIVE_PATH" -C "$TMP_DIR"

for binary in ts-skills ts-skillsd; do
  if [ ! -f "$TMP_DIR/$binary" ]; then
    echo "error: release archive did not contain $binary" >&2
    exit 1
  fi
done

for binary in ts-skills ts-skillsd; do
  binary_path="$TMP_DIR/$binary"
  if command -v install >/dev/null 2>&1; then
    install -m 0755 "$binary_path" "$INSTALL_DIR/$binary"
  else
    cp "$binary_path" "$INSTALL_DIR/$binary"
    chmod 0755 "$INSTALL_DIR/$binary"
  fi
done

printf '%s\n' 'install-script' > "$INSTALL_DIR/.ts-skills-managed-by"

echo "Installed ts-skills and ts-skillsd to $INSTALL_DIR"
case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) echo "Run: ts-skills version" ;;
  *)
    echo "Add $INSTALL_DIR to PATH, then run: ts-skills version"
    echo "For now: $INSTALL_DIR/ts-skills version"
    ;;
esac
