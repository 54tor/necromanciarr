// Package shellquote rend des arguments en mots littéraux pour un shell POSIX, sans aucune expansion possible.
//
// Contrat : valable pour un shell POSIX (sh, bash, dash, ash, zsh, ksh) ; fish interprète \' et \\ entre
// apostrophes : le shell de connexion distant doit être vérifié (voir C16) avec ShellSemanticsProbe.
package shellquote

import (
	"errors"
	"strings"
)

// ErrUnquotable signale un argument qu'aucun shell POSIX ne peut recevoir : il contient un octet NUL.
var ErrUnquotable = errors.New("shellquote: argument contains a NUL byte")

// ShellSemanticsProbe est la ligne de sonde du shell de connexion distant : printf des mots Join("a'b\\", "!x"),
// soit un guillemet simple échappé collé à une barre oblique inverse finale, puis un point d'exclamation.
const ShellSemanticsProbe = `printf %s 'a'\''b\' '!x'`

// ShellSemanticsExpected est la sortie exacte de ShellSemanticsProbe sous un shell POSIX.
const ShellSemanticsExpected = `a'b\!x`

// escapedSingleQuote ferme les guillemets simples, ajoute un ' échappé et les rouvre.
const escapedSingleQuote = `'\''`

// Quote rend un argument en un seul mot pour un shell POSIX : entre guillemets simples, chaque guillemet simple
// remplacé par la séquence qui ferme, échappe et rouvre ; la chaîne vide donne deux guillemets simples :
//
//	a'b  →  'a'\''b'
//	     →  ''
//
// Les autres octets (sauts de ligne, caractères de contrôle, UTF-8 invalide) passent tels quels, octet pour octet :
// les guillemets simples POSIX préservent tout sauf le guillemet simple et NUL. Un argument contenant NUL est refusé
// (ErrUnquotable). Aucune borne de taille : l'appelant borne ses entrées.
//
// Quote empêche l'injection de commande, pas l'injection d'option : -rf reste un mot qui commence par un tiret.
// L'appelant place -- avant les chemins, ou n'envoie que des chemins absolus.
func Quote(argument string) (string, error) {
	if strings.IndexByte(argument, 0) >= 0 {
		return "", ErrUnquotable
	}
	return "'" + strings.ReplaceAll(argument, "'", escapedSingleQuote) + "'", nil
}

// Join quote chaque argument avec Quote et les sépare d'une espace. Une liste vide donne "" ; le premier argument
// refusé rend son erreur.
func Join(arguments []string) (string, error) {
	quotedArguments := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		quoted, err := Quote(argument)
		if err != nil {
			return "", err
		}
		quotedArguments = append(quotedArguments, quoted)
	}
	return strings.Join(quotedArguments, " "), nil
}
