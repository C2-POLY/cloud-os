#!/bin/sh
# Builds ffmpeg and ffprobe for the worker's videos (#41), into <prefix>/bin:
#
#   worker/ffmpeg/build.sh <prefix>
#
# The same script builds them for the worker image (worker/Dockerfile), for
# the tests on a Mac (`make ffmpeg`) and in CI. It compiles the official
# release, unmodified, checked against its SHA-256, under the LGPL only
# (docs/decisions/002-lgpl.md): nothing GPL, nonfree or version3 is enabled,
# so no x264 or x265. Only what reading a phone video needs is built: the
# MP4/QuickTime demuxer, the native decoders of the codecs phones record, the
# filters ffmpeg uses to turn a frame upright, and a PNG encoder to hand one
# frame to Pillow. No network protocol: it reads local files and pipes only.
set -eu

VERSION=7.1.5
SHA256=de668509caf9e35e3cd162473441fdb29538c6d96ed080292b3cf9e6fc5d558f

prefix=${1:?usage: build.sh <prefix>}
mkdir -p "$prefix"
prefix=$(cd "$prefix" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

curl -fsSL "https://ffmpeg.org/releases/ffmpeg-$VERSION.tar.xz" -o "$work/ffmpeg.tar.xz"
if command -v sha256sum >/dev/null; then
	echo "$SHA256  $work/ffmpeg.tar.xz" | sha256sum -c -
else
	echo "$SHA256  $work/ffmpeg.tar.xz" | shasum -a 256 -c -
fi
tar -xJf "$work/ffmpeg.tar.xz" -C "$work"
cd "$work/ffmpeg-$VERSION"

./configure \
	--prefix="$prefix" \
	--disable-everything --disable-autodetect --disable-network \
	--disable-doc --disable-debug --disable-ffplay --disable-x86asm \
	--enable-ffmpeg --enable-ffprobe \
	--enable-zlib \
	--enable-protocol=file,pipe \
	--enable-demuxer=mov \
	--enable-decoder=h264,hevc,mpeg4,prores,vp9,mjpeg \
	--enable-parser=h264,hevc,mpeg4video,vp9,mjpeg \
	--enable-encoder=png \
	--enable-muxer=image2pipe \
	--enable-filter=buffer,buffersink,format,scale,transpose,hflip,vflip,null
make -j"$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)"
make install

# The licence guard: fail the build if anything but the LGPL got in.
conf=$("$prefix/bin/ffmpeg" -hide_banner -buildconf)
if printf '%s\n' "$conf" | grep -E -- '--enable-(gpl|nonfree|version3|libx264|libx265)'; then
	echo "ffmpeg is not LGPL-only" >&2
	exit 1
fi
"$prefix/bin/ffmpeg" -hide_banner -L | head -n 3
