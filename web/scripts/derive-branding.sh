#!/usr/bin/env bash
# Régénère les logos de l'interface à partir des originaux de assets/branding/ (jamais modifiés).
# Usage : web/scripts/derive-branding.sh   (ImageMagick 7 requis : magick)
set -euo pipefail

racine="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source_dir="${racine}/assets/branding"
sortie="${racine}/web/public/branding"
mkdir -p "${sortie}"

# derive <original> <fichier> <taille en px> [options magick...]
derive() {
	local original="$1" fichier="$2" taille="$3"
	shift 3
	magick "${source_dir}/${original}" -colorspace sRGB -resize "${taille}x${taille}" \
		-strip "$@" "${sortie}/${fichier}"
}

# Affichage 144 px : 2x ; l'alpha est conservé.
derive logo-complet.png logo-complet.webp 288 -quality 85 -define webp:alpha-quality=90
# Affichage 32 px : 2x.
derive embleme.png embleme.webp 64 -quality 90 -define webp:alpha-quality=90
# Favicon et apple-touch-icon restent en PNG (chemins de navigateur sans WebP).
derive icone.png favicon-32.png 32 -define png:compression-level=9
derive icone.png apple-touch-icon.png 180 -define png:compression-level=9

cd "${racine}/web" && sha256sum public/branding/*
