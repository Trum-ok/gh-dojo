#!/bin/sh
# Renders the README media next to this script: demo-{light,dark}.gif (the
# main demo) and compare-{light,dark}.gif (gh against gh dojo).
# Needs Node (see package.json engines) and ffmpeg; run `npm ci` first.
set -eu
cd "$(dirname "$0")"

FPS=25 # the compositions' rate (src/Root.tsx), so no frame is dropped

for scene in Demo Compare; do
	for theme in Light Dark; do
		id="$scene$theme"
		gif="$(printf %s "$scene-$theme" | tr A-Z a-z).gif"
		# Lossless frames: a lossy video in between adds noise to every frame,
		# and the GIF then stores whole frames instead of what changed.
		rm -rf "out/$id"
		npx remotion render "$id" "out/$id" --sequence --image-format=png --log=error
		ffmpeg -loglevel error -y -framerate "$FPS" -pattern_type glob -i "out/$id/*.png" -filter_complex \
			"split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
			"$gif"
		rm -rf "out/$id"
		echo "$gif $(du -h "$gif" | cut -f1)"
	done
done
