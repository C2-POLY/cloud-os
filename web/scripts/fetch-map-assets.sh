#!/bin/sh
# Fetches the map's fonts and sprites (#40) into public/map, from a pinned
# commit of github.com/protomaps/basemaps-assets: the box then serves them
# itself and the map loads nothing from elsewhere. Run it again only to move
# to another commit; its output is committed.
#
# Fonts: Noto Sans, SIL Open Font License 1.1 (docs/decisions/004-fonts-ofl.md),
# Latin, Latin extended and punctuation ranges only. Sprites: derived from the
# MIT-licensed tangrams/icons.
set -eu

COMMIT=028c18f713baecad011301ff7a69acc39bcc2ae7
BASE=https://raw.githubusercontent.com/protomaps/basemaps-assets/$COMMIT
out=$(cd "$(dirname "$0")/.." && pwd)/public/map

fetch() { # <url path> <file>
	mkdir -p "$(dirname "$2")"
	curl -fsSL "$BASE/$(printf '%s' "$1" | sed 's/ /%20/g')" -o "$2"
}

for font in 'Noto Sans Regular' 'Noto Sans Medium' 'Noto Sans Italic'; do
	for range in 0-255 256-511 8192-8447; do
		fetch "fonts/$font/$range.pbf" "$out/fonts/$font/$range.pbf"
	done
done
fetch fonts/OFL.txt "$out/fonts/OFL.txt"
for flavor in light dark; do
	for f in "$flavor.json" "$flavor.png" "$flavor@2x.json" "$flavor@2x.png"; do
		fetch "sprites/v4/$f" "$out/sprites/$f"
	done
done
curl -fsSL https://raw.githubusercontent.com/tangrams/icons/master/LICENSE.md -o "$out/sprites/LICENSE.md"
echo "map assets from basemaps-assets $COMMIT in $out"
