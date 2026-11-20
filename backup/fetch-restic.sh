#!/bin/sh
# Fetch restic (BSD-2-Clause) into DEST, checked against its SHA-256.
#
#   backup/fetch-restic.sh DEST [OS ARCH]
#
# The version and the sums are pinned here, from the release's SHA256SUMS
# (https://github.com/restic/restic/releases). Used by backup/Dockerfile;
# OS and ARCH default to this machine's (linux amd64, linux arm64, darwin arm64).

set -eu

VERSION=0.19.1

dest=${1:?usage: fetch-restic.sh DEST [OS ARCH]}
os=${2:-$(uname -s | tr '[:upper:]' '[:lower:]')}
arch=${3:-$(uname -m)}
case $arch in x86_64) arch=amd64 ;; aarch64) arch=arm64 ;; esac

case ${os}_$arch in
linux_amd64) sum=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c ;;
linux_arm64) sum=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465 ;;
darwin_arm64) sum=7be0a144ccc377880f294204aa271d76e4b79554b42a751151d425ce6ebac143 ;;
*) echo "fetch-restic.sh: no pinned restic for ${os}_$arch" >&2; exit 1 ;;
esac

name=restic_${VERSION}_${os}_$arch.bz2
url=https://github.com/restic/restic/releases/download/v$VERSION/$name
mkdir -p "$dest"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if command -v curl >/dev/null 2>&1; then
	curl --fail --silent --show-error --location --output "$tmp/$name" "$url"
else
	wget -q -O "$tmp/$name" "$url"
fi
if command -v sha256sum >/dev/null 2>&1; then
	echo "$sum  $tmp/$name" | sha256sum -c - >/dev/null
else
	echo "$sum  $tmp/$name" | shasum -a 256 -c - >/dev/null
fi
bunzip2 -c "$tmp/$name" >"$tmp/restic"
chmod 0755 "$tmp/restic"
mv "$tmp/restic" "$dest/restic"
"$dest/restic" version
