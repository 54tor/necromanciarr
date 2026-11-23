#!/usr/bin/env bash
# Refuse tout chemin personnel ou adresse e-mail réelle dans les sources du site.
set -euo pipefail

dossier="${1:?usage: garde-identite.test.sh <dossier>}"
cheminsAutorises='/home/(u|bob|alice|user)/'

occurrences=$(grep -rnIE --include='*.md' --include='*.mts' --include='*.vue' --include='*.ts' \
	--exclude-dir=node_modules --exclude-dir=dist --exclude-dir=cache \
	-e '/home/[a-z]' -e '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[a-z]{2,}' "$dossier" |
	grep -vE "$cheminsAutorises" |
	grep -vE '@[A-Za-z0-9.-]*example\.(com|org)' || true)

if [ -n "$occurrences" ]; then
	echo "identité ou chemin local détecté :" >&2
	echo "$occurrences" >&2
	exit 1
fi
