#!/bin/sh
# Garde de commande forcée pour la clé SSH de necromanciarr : seuls le serveur SFTP et les lignes exactes de la table
# des commandes distantes (stat, sha1sum, md5sum, en lecture seule) passent ; tout le reste est refusé (code 126).
#
# Installation sur le serveur, pour un compte dédié en lecture seule sur la bibliothèque et en écriture dans le seul
# dossier de liens :
#   1. copier ce script, par exemple en /usr/local/bin/necromanciarr-guard (propriétaire root, mode 0755) ;
#   2. dans sshd_config, « Subsystem sftp internal-sftp » : une requête SFTP arrive alors ici sous la ligne
#      internal-sftp ;
#   3. dans ~/.ssh/authorized_keys du compte, une seule ligne pour la clé de necromanciarr :
#      restrict,command="/usr/local/bin/necromanciarr-guard" ssh-ed25519 AAAA... necromanciarr
#      (restrict coupe pseudo-terminal, redirections de ports, agent et X11).
# Les lignes autorisées sont celles de « necromanciarr volumes commands <nom> » ; un test du dépôt les compare aux
# constantes du code. Une autre version de necromanciarr peut en changer : reprendre le script de la même version.

set -u

# Serveur SFTP lancé pour la requête internal-sftp ; son chemin dépend de la distribution (Debian et Ubuntu : celui-ci,
# Fedora et RHEL : /usr/libexec/openssh/sftp-server, Arch : /usr/lib/ssh/sftp-server).
sftp_server=/usr/lib/openssh/sftp-server

tab=$(printf '\t')
requested=${SSH_ORIGINAL_COMMAND:-}

# is_allowed compare la ligne reçue, octet pour octet, aux lignes constantes de la table.
is_allowed() {
	case "$1" in
	# début des lignes autorisées (générées depuis sftpfs.GuardLines, ne pas modifier à la main)
	"printf %s 'a'\\''b\\' '!x'") return 0 ;;
	"'stat' '--version'") return 0 ;;
	"'xargs' '--version'") return 0 ;;
	"'sha1sum' '--version'") return 0 ;;
	"'md5sum' '--version'") return 0 ;;
	"'nice' '--version'") return 0 ;;
	"'ionice' '--version'") return 0 ;;
	"'xargs' '-0' '-r' 'stat' '--printf=%d${tab}%i${tab}%h${tab}%s${tab}%Y\\0' '--'") return 0 ;;
	"'xargs' '-0' '-r' 'sha1sum' '-z' '--'") return 0 ;;
	"'xargs' '-0' '-r' 'md5sum' '-z' '--'") return 0 ;;
	"'nice' '-n' '10' 'ionice' '-c3' 'xargs' '-0' '-r' 'sha1sum' '-z' '--'") return 0 ;;
	"'nice' '-n' '10' 'ionice' '-c3' 'xargs' '-0' '-r' 'md5sum' '-z' '--'") return 0 ;;
	# fin des lignes autorisées
	esac
	return 1
}

if [ "$requested" = internal-sftp ] || [ "$requested" = "$sftp_server" ]; then
	exec "$sftp_server"
fi
if is_allowed "$requested"; then
	# La ligne est une constante de la table : son texte ne vient jamais d'un nom de fichier.
	exec /bin/sh -c "$requested"
fi
echo "necromanciarr-guard: command refused" >&2
exit 126
