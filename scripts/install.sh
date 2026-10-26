#!/bin/sh
# Installs the `enclave` program from an EnclaveStack release on GitHub (ADR-054, L5-004 §1a):
#
#   curl -fsSL https://enclavestack.soupler.com/install.sh | sh
#
# Settings, all optional, go before `sh`:
#   ENCLAVE_VERSION=v0.9.0       a release instead of the latest one
#   ENCLAVE_INSTALL_DIR=~/bin    where `enclave` goes (default: /usr/local/bin if writable, else ~/.local/bin)
#   ENCLAVE_DOWNLOAD_BASE=URL    a mirror of https://github.com/EnclaveStack/platform/releases
#   ENCLAVE_OS, ENCLAVE_ARCH     skip detecting the system (darwin|linux, amd64|arm64)
#
# It installs nothing unless the archive matches the release's SHA256SUMS, and checks the signature of
# SHA256SUMS too when cosign is installed. The source is scripts/install.sh in EnclaveStack/platform;
# the website serves a copy.
set -eu

base="${ENCLAVE_DOWNLOAD_BASE:-https://github.com/EnclaveStack/platform/releases}"
base="${base%/}"

say() { printf '%s\n' "$*"; }
fail() {
	printf 'enclave install: %s\n' "$*" >&2
	exit 1
}
need() { command -v "$1" >/dev/null 2>&1 || fail "needs $1, which isn't installed"; }

need curl
need tar
need uname

os="${ENCLAVE_OS:-$(uname -s)}"
case "$os" in
Darwin | darwin) os=darwin ;;
Linux | linux) os=linux ;;
*) fail "EnclaveStack runs on macOS and Linux, not $os" ;;
esac
arch="${ENCLAVE_ARCH:-$(uname -m)}"
case "$arch" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "EnclaveStack runs on amd64 (x86_64) and arm64 (aarch64) processors, not $arch" ;;
esac

version="${ENCLAVE_VERSION:-}"
if [ -z "$version" ]; then
	# releases/latest redirects to …/tag/vX.Y.Z
	latest=$(curl -fsSL -o /dev/null -w '%{url_effective}' "$base/latest") || fail "couldn't find the latest release at $base/latest"
	version="${latest##*/}"
fi
case "$version" in v*) ;; *) version="v$version" ;; esac
case "$version" in
v[0-9]*.[0-9]*.[0-9]*) ;;
*) fail "a version looks like v1.2.3, not $version" ;;
esac
number="${version#v}"
archive="enclave_${number}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Installing enclave $number for $os/$arch"
curl -fsSL -o "$tmp/$archive" "$base/download/$version/$archive" ||
	fail "couldn't download $archive: check that $version exists at $base"
curl -fsSL -o "$tmp/SHA256SUMS" "$base/download/$version/SHA256SUMS" ||
	fail "couldn't download SHA256SUMS for $version"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$expected" ] || fail "SHA256SUMS has no line for $archive"
if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "$tmp/$archive" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "$tmp/$archive" | awk '{ print $1 }')
else
	fail "needs sha256sum or shasum to check the download"
fi
[ "$expected" = "$actual" ] || fail "checksum mismatch for $archive (expected $expected, got $actual); nothing was installed"
say "Checksum OK"

if command -v cosign >/dev/null 2>&1; then
	curl -fsSL -o "$tmp/SHA256SUMS.sigstore.json" "$base/download/$version/SHA256SUMS.sigstore.json" ||
		fail "couldn't download the signature of SHA256SUMS"
	cosign verify-blob "$tmp/SHA256SUMS" --bundle "$tmp/SHA256SUMS.sigstore.json" \
		--certificate-identity-regexp '^https://github.com/EnclaveStack/platform/\.github/workflows/release\.yml@refs/tags/v' \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com >/dev/null 2>&1 ||
		fail "the signature of SHA256SUMS doesn't check out; nothing was installed"
	say "Signature OK"
else
	say "Skipped the signature check: cosign isn't installed"
fi

tar -xzf "$tmp/$archive" -C "$tmp" enclave || fail "couldn't unpack $archive"

dir="${ENCLAVE_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
	if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir="$HOME/.local/bin"
	fi
fi
mkdir -p "$dir" || fail "couldn't create $dir"
cp "$tmp/enclave" "$dir/enclave.new" && chmod 0755 "$dir/enclave.new" && mv -f "$dir/enclave.new" "$dir/enclave" ||
	fail "couldn't write $dir/enclave; pick another folder with ENCLAVE_INSTALL_DIR"

installed=$("$dir/enclave" version 2>/dev/null) || fail "installed $dir/enclave, but it doesn't run on this computer"
say "Installed $dir/enclave ($installed)"
case ":$PATH:" in
*":$dir:"*) ;;
*) say "$dir isn't on your PATH yet: add  export PATH=\"$dir:\$PATH\"  to your shell's profile, then open a new terminal" ;;
esac
say "Next: start Docker, then run  enclave server  and open http://127.0.0.1:7070"
