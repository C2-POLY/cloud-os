#!/bin/sh
# A cut during an upload loses nothing (docs/architecture/box.md, « Installation
# et démarrage »; issue #103). On a throwaway production box of its own:
#
#   1. a resumable upload (tus) of 200 MB starts;
#   2. halfway, db and core are killed (docker kill -s KILL), as a power cut
#      kills them;
#   3. after scripts/box up, core verify passes, the upload resumes where the
#      disk says it stands and ends with the same SHA-256, and no partial file
#      shows in the tree.
#
# Run by `make test-crash` and by the CI images workflow, on amd64 and arm64.
# It builds the images (db and core only). It never touches another box:
# CLOUDOS_BOX_PROJECT (default cloud-os-crash) names its Compose project,
# CLOUDOS_CORE_PORT (default 8494) its port, and its box.env and data live
# in a temporary folder, removed at the end with the project's volumes.

set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
box=$root/scripts/box
SIZE=$((200 * 1024 * 1024))
UPLOAD_PATH=Envois/coupure.bin

export CLOUDOS_BOX_PROJECT="${CLOUDOS_BOX_PROJECT:-cloud-os-crash}"
export CLOUDOS_CORE_PORT="${CLOUDOS_CORE_PORT:-8494}"
case $CLOUDOS_BOX_PROJECT in
cloud-os-box | cloud-os) echo "test-crash: $CLOUDOS_BOX_PROJECT is a real stack: name a throwaway project" >&2; exit 1 ;;
esac
# Its end removes the project's volumes: never one that exists already.
if [ -n "$(docker ps -aq --filter "label=com.docker.compose.project=$CLOUDOS_BOX_PROJECT")$(docker volume ls -q --filter "label=com.docker.compose.project=$CLOUDOS_BOX_PROJECT")" ]; then
	echo "test-crash: the project $CLOUDOS_BOX_PROJECT already exists: name a new one with CLOUDOS_BOX_PROJECT" >&2
	exit 1
fi

work=$(mktemp -d)
chmod 700 "$work"
export CLOUDOS_BOX_ENV="$work/box.env" CLOUDOS_DATA_DIR="$work/files" CLOUDOS_MAPS_DIR="$work/maps"
url=http://127.0.0.1:$CLOUDOS_CORE_PORT
sender=

cleanup() {
	[ -z "$sender" ] || kill "$sender" 2>/dev/null || true
	if [ -f "$CLOUDOS_BOX_ENV" ]; then
		"$box" down -v --remove-orphans >/dev/null 2>&1 || true
	fi
	# The data folders are the core's (uid 65532) on Linux.
	rm -rf "$work" 2>/dev/null || sudo -n rm -rf "$work" 2>/dev/null || echo "test-crash: left $work" >&2
}
trap cleanup EXIT
trap 'exit 1' INT TERM

say() { printf 'test-crash: %s\n' "$*"; }
die() {
	printf 'test-crash: FAIL: %s\n' "$*" >&2
	if [ -f "$CLOUDOS_BOX_ENV" ]; then
		"$box" logs --no-color --tail 50 core db >&2 2>&1 || true
	fi
	exit 1
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
b64() { printf '%s' "$1" | base64 | tr -d '\n'; }

# The upload's offset as the core reads it from the disk (HEAD), or nothing.
offset() {
	curl -s -I -H "@$work/auth" -H 'Tus-Resumable: 1.0.0' "$url$location" |
		tr -d '\r' | sed -n 's/^[Uu]pload-[Oo]ffset: //p'
}

say "box $CLOUDOS_BOX_PROJECT on port $CLOUDOS_CORE_PORT, files in $work/files"
"$box" init >/dev/null
"$box" up core || die "the box did not start"

# The account, with a password drawn here and never shown. The password and
# the session go to curl in files of this folder, never on a command line.
(
	umask 077
	printf '{"name":"coupure","display_name":"Coupure","password":"%s"}' "$(LC_ALL=C tr -dc 'A-Za-z0-9' </dev/urandom | head -c 32)" >"$work/setup.json"
)
token=$(curl -sf -H 'Content-Type: application/json' -d "@$work/setup.json" "$url/api/setup" |
	sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || die "no account was created"
(umask 077 && printf 'Authorization: Bearer %s\n' "$token" >"$work/auth")
unset token

head -c "$SIZE" /dev/urandom >"$work/source.bin"
want=$(sha256 "$work/source.bin")
location=$(curl -sf -o /dev/null -D - -X POST -H "@$work/auth" -H 'Tus-Resumable: 1.0.0' \
	-H "X-Cloudos-Client: test-crash" -H "Upload-Length: $SIZE" \
	-H "Upload-Metadata: path $(b64 "$UPLOAD_PATH"),sha256 $(b64 "$want")" "$url/api/uploads" |
	tr -d '\r' | sed -n 's/^[Ll]ocation: //p')
case $location in /api/uploads/*) ;; *) die "the upload was not created" ;; esac

# 1. The whole file in one request, slowed down so that the cut falls halfway.
curl -s -o /dev/null --limit-rate 20M -X PATCH -H "@$work/auth" -H 'Tus-Resumable: 1.0.0' \
	-H "X-Cloudos-Client: test-crash" -H 'Content-Type: application/offset+octet-stream' -H 'Upload-Offset: 0' \
	--data-binary "@$work/source.bin" "$url$location" &
sender=$!
half=$((SIZE / 2))
i=0
while :; do
	o=$(offset)
	[ -z "$o" ] || [ "$o" -lt "$half" ] || break
	i=$((i + 1))
	[ "$i" -lt 600 ] || die "the upload never reached half its size (offset ${o:-none})"
	sleep 0.1
done

# 2. The cut: db and core die at once, mid-write.
ids=$("$box" ps -q core db)
[ "$(printf '%s\n' "$ids" | grep -c .)" -eq 2 ] || die "cannot find the db and core containers"
# shellcheck disable=SC2086 # two container ids
docker kill -s KILL $ids >/dev/null
wait "$sender" || true
sender=
say "cut db and core at $o of $SIZE bytes"

# 3. Back up: nothing lost, nothing half-written in the tree.
"$box" up core || die "the box did not come back after the cut"
"$box" run --rm core verify || die "core verify found a difference after the cut"
resume=$(offset)
case $resume in '' | *[!0-9]*) die "the upload is gone after the cut" ;; esac
[ "$resume" -gt 0 ] && [ "$resume" -lt "$SIZE" ] || die "the upload resumes at $resume of $SIZE"
say "the upload resumes at $resume"
tail -c +"$((resume + 1))" "$work/source.bin" >"$work/rest.bin"
status=$(curl -s -o "$work/patch.out" -w '%{http_code}' -X PATCH -H "@$work/auth" -H 'Tus-Resumable: 1.0.0' \
	-H "X-Cloudos-Client: test-crash" -H 'Content-Type: application/offset+octet-stream' -H "Upload-Offset: $resume" \
	--data-binary "@$work/rest.bin" "$url$location")
[ "$status" = 204 ] || die "the end of the upload answered $status: $(cat "$work/patch.out")"

curl -sf -H "@$work/auth" -o "$work/stored.bin" "$url/api/files/content?path=$UPLOAD_PATH" ||
	die "the uploaded file cannot be read back"
got=$(sha256 "$work/stored.bin")
[ "$got" = "$want" ] || die "the stored file's SHA-256 is $got, not $want"
names=$(curl -sf -H "@$work/auth" "$url/api/files?path=Envois" | grep -o '"name":"[^"]*"' || true)
[ "$names" = '"name":"coupure.bin"' ] || die "the folder holds $names, not the one file"
"$box" run --rm core verify || die "core verify found a difference after the upload"
say "ok: after the cut, core verify passes and the upload ends with the same SHA-256 ($want)"
