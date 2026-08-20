#!/bin/sh
# tennis installer. Fetches the release archive for this OS/arch from the
# download host, verifies it against the checksums the release was published
# with, and drops the binary in a bin directory.
#
#   curl -fsSL https://TENNIS_DOWNLOAD_HOST/install.sh | sh
#
# The archive is fetched by version rather than from a "latest" alias: the
# versioned key is immutable, so it caches at the CDN edge forever, and its
# name matches the entry in the checksums.txt goreleaser signed it into. The
# alias under /latest exists for Dockerfiles that want to skip the lookup.
set -eu

base_url="${TENNIS_INSTALL_BASE_URL:-https://TENNIS_DOWNLOAD_HOST}"
install_dir="${TENNIS_INSTALL_DIR:-$HOME/.local/bin}"
version="${TENNIS_VERSION:-}"

fail() {
  echo "tennis install: $*" >&2
  exit 1
}

usage() {
  cat <<EOF
tennis installer

Downloads the tennis binary for this platform, checks it against the
published SHA-256, and installs it.

Usage:
  curl -fsSL $base_url/install.sh | sh

Options:
  -h, --help      Show this help

Environment:
  TENNIS_VERSION           Version to install, e.g. 0.1.0 (default: latest)
  TENNIS_INSTALL_DIR       Install directory (default: ~/.local/bin)
  TENNIS_INSTALL_BASE_URL  Download base URL (default: $base_url)
EOF
}

for arg in "$@"; do
  case "$arg" in
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $arg (try --help)" ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

need tar
need awk
need install

download() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$2" "$1"
  else
    fail "missing curl or wget"
  fi
}

# Print the SHA-256 of a file, on macOS (shasum) or GNU (sha256sum).
sha256_of() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    fail "missing shasum or sha256sum"
  fi
}

# goreleaser names archives by GOOS/GOARCH, so the install names must be
# spelled the Go way rather than the uname way.
case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *) fail "unsupported OS: $(uname -s) (tennis ships darwin and linux)" ;;
esac

case "$(uname -m)" in
  arm64|aarch64) arch="arm64" ;;
  x86_64|amd64) arch="amd64" ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

base_url="${base_url%/}"
tmp_dir="$(mktemp -d 2>/dev/null || mktemp -d -t tennis-install)"
cleanup() { rm -rf "$tmp_dir"; }
trap cleanup EXIT INT TERM

if [ -z "$version" ]; then
  download "$base_url/VERSION" "$tmp_dir/VERSION" ||
    fail "cannot reach $base_url/VERSION"
  # Trim whitespace and any stray CR so a hand-edited VERSION still works.
  version="$(tr -d ' \t\r\n' < "$tmp_dir/VERSION")"
fi
version="${version#v}"
[ -n "$version" ] || fail "could not determine the version to install"

archive="tennis_${version}_${os}_${arch}.tar.gz"
archive_url="$base_url/v$version/$archive"
archive_path="$tmp_dir/$archive"

echo "Downloading $archive_url"
download "$archive_url" "$archive_path" || fail "no release at $archive_url"
download "$base_url/v$version/checksums.txt" "$tmp_dir/checksums.txt" ||
  fail "no checksums for v$version"

expected="$(awk -v want="$archive" '$2 == want || $2 == "*" want {print $1}' "$tmp_dir/checksums.txt")"
[ -n "$expected" ] || fail "$archive is not listed in checksums.txt"

actual="$(sha256_of "$archive_path")"
if [ "$expected" != "$actual" ]; then
  fail "checksum mismatch for $archive (expected $expected, got $actual)"
fi

tar -xzf "$archive_path" -C "$tmp_dir" tennis ||
  fail "archive does not contain a tennis binary"

mkdir -p "$install_dir"
install -m 755 "$tmp_dir/tennis" "$install_dir/tennis"

echo "Installed tennis $version to $install_dir/tennis"

# A fresh ~/.local/bin is often not on PATH yet, and the failure that causes
# ("command not found" right after a successful install) is worth pre-empting.
case ":$PATH:" in
  *":$install_dir:"*) ;;
  *) echo "Add $install_dir to your PATH:  export PATH=\"$install_dir:\$PATH\"" ;;
esac

echo "The embedding model (~123MB) downloads once on first use."
"$install_dir/tennis" version
