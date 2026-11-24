#!/usr/bin/env bash
# Refuse, dans la référence, un chemin personnel, une adresse e-mail réelle ou un lien relatif qui ne mène nulle part.
# Mêmes règles d'identité que docs/site/garde-identite.test.sh.
set -euo pipefail

dossier="${1:?usage: verifier-docs.test.sh <dossier>}"
cheminsAutorises='/home/(u|bob|alice|user)/'

occurrences=$(grep -rnIE --include='*.md' \
	-e '/home/[a-z]' -e '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}' "$dossier" |
	grep -vE "$cheminsAutorises" |
	grep -vE '@[A-Za-z0-9.-]*example\.(com|org)' || true)

if [ -n "$occurrences" ]; then
	echo "identité ou chemin local détecté :" >&2
	echo "$occurrences" >&2
	exit 1
fi

liensCasses=""
while IFS= read -r fichier; do
	repertoire=$(dirname "$fichier")
	while IFS= read -r cible; do
		cible="${cible%%#*}"
		if [ -n "$cible" ] && [ ! -e "$repertoire/$cible" ]; then
			liensCasses+="$fichier -> $cible"$'\n'
		fi
	done < <(grep -oE '\]\([^)#][^)]*\)' "$fichier" | sed -E 's/^\]\((.*)\)$/\1/' | grep -vE '^(https?:|mailto:)' || true)
done < <(find "$dossier" -name '*.md')

if [ -n "$liensCasses" ]; then
	echo "lien relatif cassé :" >&2
	printf '%s' "$liensCasses" >&2
	exit 1
fi
