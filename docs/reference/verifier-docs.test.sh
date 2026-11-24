#!/usr/bin/env bash
# Refuse, dans la référence, un chemin personnel, une adresse e-mail réelle ou un lien relatif qui ne mène nulle part.
# Mêmes règles d'identité que docs/site/garde-identite.test.sh.
set -euo pipefail

dossier="${1:?usage: verifier-docs.test.sh <dossier>}"

# Cas reference_en_parity : docs/reference et docs/reference/en ont les mêmes fichiers, le même nombre de titres par
# niveau et les mêmes éléments entre accents graves. Les blocs de code délimités par ``` sont ignorés (titres et
# éléments) : leurs commentaires peuvent rester en français. La première différence est affichée, puis exit 1.
horsBlocsDeCode() {
	awk '/^```/ { dansBloc = !dansBloc; next } !dansBloc' "$1"
}

titresParNiveau() {
	horsBlocsDeCode "$1" | grep -oE '^#{1,6} ' | awk '{ print length($1) }' | sort | uniq -c || true
}

elementsEnCode() {
	# shellcheck disable=SC2016
	horsBlocsDeCode "$1" | grep -oE '`[^`]+`' | sort -u || true
}

echecParite() {
	echo "parité fr/en : $1" >&2
	exit 1
}

reference_en_parity() {
	local fr="$dossier" en="$dossier/en" fichier difference
	[ -d "$en" ] || echecParite "dossier absent : $en"
	difference=$(diff <(cd "$fr" && ls -- *.md) <(cd "$en" && ls -- *.md 2>/dev/null) | head -n 1 || true)
	[ -z "$difference" ] || echecParite "liste de fichiers différente : $difference"
	while IFS= read -r fichier; do
		fichier=$(basename "$fichier")
		difference=$(diff <(titresParNiveau "$fr/$fichier") <(titresParNiveau "$en/$fichier") | head -n 1 || true)
		[ -z "$difference" ] || echecParite "$fichier : nombre de titres par niveau différent : $difference"
		difference=$(diff <(elementsEnCode "$fr/$fichier") <(elementsEnCode "$en/$fichier") | grep -E '^[<>]' | head -n 1 || true)
		[ -z "$difference" ] || echecParite "$fichier : élément entre accents graves différent : $difference"
	done < <(find "$fr" -maxdepth 1 -name '*.md')
}

reference_en_parity

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
