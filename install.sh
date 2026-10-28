#!/bin/sh

set -eu

REPOSITORY="${CARETZ_REPOSITORY:-makkalot/caretz}"
INSTALL_DIR="${CARETZ_INSTALL_DIR:-${HOME}/bin}"

fail() {
	printf 'caretz installer: %s\n' "$*" >&2
	exit 1
}

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -n "${CARETZ_VERSION:-}" ]; then
	version=$CARETZ_VERSION
else
	latest_url=$(curl -fsSL -o /dev/null -w '%{url_effective}' \
		"https://github.com/${REPOSITORY}/releases/latest") || fail "could not find the latest release"
	version=${latest_url##*/}
	[ -n "$version" ] && [ "$version" != "latest" ] || fail "could not determine the latest release version"
fi

archive="caretz-${version}-${os}-${arch}.tar.gz"
download_url="https://github.com/${REPOSITORY}/releases/download/${version}"
tmp_dir=$(mktemp -d 2>/dev/null || mktemp -d -t caretz-install) || fail "could not create a temporary directory"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

printf 'Downloading caretz %s for %s/%s...\n' "$version" "$os" "$arch"
curl -fsSL "${download_url}/${archive}" -o "${tmp_dir}/${archive}" || fail "could not download ${archive}"
curl -fsSL "${download_url}/checksums.txt" -o "${tmp_dir}/checksums.txt" || fail "could not download checksums.txt"

expected=$(awk -v name="$archive" '$2 == name || $2 == "*" name { print $1; exit }' "${tmp_dir}/checksums.txt")
[ -n "$expected" ] || fail "no checksum found for ${archive}"

if command -v sha256sum >/dev/null 2>&1; then
	actual=$(sha256sum "${tmp_dir}/${archive}" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	actual=$(shasum -a 256 "${tmp_dir}/${archive}" | awk '{print $1}')
else
	fail "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || fail "checksum verification failed for ${archive}"

tar -xzf "${tmp_dir}/${archive}" -C "$tmp_dir" ./caretz || fail "could not extract caretz"
mkdir -p "$INSTALL_DIR" || fail "could not create ${INSTALL_DIR}"
install -m 0755 "${tmp_dir}/caretz" "${INSTALL_DIR}/caretz" || fail "could not install to ${INSTALL_DIR}/caretz"

printf 'Installed caretz %s to %s/caretz\n' "$version" "$INSTALL_DIR"
case ":${PATH}:" in
	*":${INSTALL_DIR}:"*) ;;
	*) printf 'Add %s to your PATH to run caretz from any directory.\n' "$INSTALL_DIR" ;;
esac
