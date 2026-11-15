#!/bin/sh
# Renders the README media next to this script: demo-{light,dark}.gif (the
# main demo) and compare-{light,dark}.gif (gh against gh dojo).
# Needs Node (see package.json engines) and ffmpeg; run `npm ci` first.
set -eu
cd "$(dirname "$0")"

FPS=${FPS:-15}

mkdir -p out
for scene in Demo Compare; do
	for theme in Light Dark; do
		gif="$(printf %s "$scene-$theme" | tr A-Z a-z).gif"
		npx remotion render "$scene$theme" "out/$scene$theme.mp4" --codec=h264 --crf=8 --log=error
		# A palette made for this video, and only the changed part of each frame stored.
		ffmpeg -loglevel error -y -i "out/$scene$theme.mp4" -filter_complex \
			"fps=$FPS,split[a][b];[a]palettegen=stats_mode=diff[p];[b][p]paletteuse=dither=sierra2_4a:diff_mode=rectangle" \
			"$gif"
		echo "$gif $(du -h "$gif" | cut -f1)"
	done
done
