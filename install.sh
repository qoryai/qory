#!/bin/sh
# Install qory from its GitHub releases. No Go toolchain needed.
#
#   curl -fsSL https://raw.githubusercontent.com/qoryai/qory/main/install.sh | sh
#
# Environment:
#   QORY_VERSION   the tag to install, such as v0.1.0. Default: the latest release.
#   QORY_BIN_DIR   where to put the binary. Default: ~/.local/bin.
set -eu

repo=qoryai/qory
version=${QORY_VERSION:-latest}
bin_dir=${QORY_BIN_DIR:-$HOME/.local/bin}

fail() {
	echo "install: $1" >&2
	exit 1
}

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "qory has no release build for $(uname -s); build from source with: go install github.com/$repo@latest" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "qory has no release build for $(uname -m); build from source with: go install github.com/$repo@latest" ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"

if [ "$version" = latest ]; then
	version=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)
	[ -n "$version" ] || fail "could not read the latest release of $repo"
fi

number=${version#v}
archive="qory_${number}_${os}_${arch}.tar.gz"
base="https://github.com/$repo/releases/download/$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "qory $version for $os $arch"
curl -fsSL "$base/$archive" -o "$tmp/$archive" || fail "no archive $archive in release $version"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail "release $version has no checksums.txt"

if command -v sha256sum >/dev/null 2>&1; then
	sum=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
elif command -v shasum >/dev/null 2>&1; then
	sum=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
else
	fail "neither sha256sum nor shasum is available to verify the download"
fi
grep -q "^$sum  $archive\$" "$tmp/checksums.txt" || fail "checksum of $archive does not match the release"

tar -xzf "$tmp/$archive" -C "$tmp" qory
mkdir -p "$bin_dir"
mv "$tmp/qory" "$bin_dir/qory"
chmod +x "$bin_dir/qory"

echo "installed $bin_dir/qory"
case ":$PATH:" in
*":$bin_dir:"*) "$bin_dir/qory" version ;;
*) echo "add it to your PATH:  export PATH=\"$bin_dir:\$PATH\"" ;;
esac
