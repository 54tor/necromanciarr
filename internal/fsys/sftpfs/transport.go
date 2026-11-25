package sftpfs

import (
	"errors"
	"io"
	"io/fs"
	"net"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// sendFailurePrefix : début du texte des échecs d'envoi de pkg/sftp v1.13.13 (packet.go : « failed to send packet »
// et « failed to send packet payload »), qui enveloppent l'erreur d'écriture du canal sans type propre.
const sendFailurePrefix = "failed to send packet"

// isTransportFailure reconnaît une connexion perdue par les types de la chaîne d'erreurs, sans dépendre du moment
// où la fin de session est remarquée : un io.EOF d'envoi n'est jamais une fin de fichier. Aucun texte d'ensemble
// n'est lu : un nom de fichier ou un message du serveur ne casse jamais le volume.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	var exitMissing *ssh.ExitMissingError
	isTyped := errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) || errors.As(err, &exitMissing)
	return isTyped || hasSendFailure(err)
}

// hasSendFailure parcourt la chaîne (Unwrap simple et multiple) à la recherche d'un maillon d'envoi de pkg/sftp. Elle
// ne descend jamais sous une erreur qui porte un chemin ou un texte du serveur (*fs.PathError, *sftp.StatusError).
func hasSendFailure(err error) bool {
	_, isPathError := err.(*fs.PathError)      //nolint:errorlint // ce maillon seul, jamais la chaîne
	_, isServerText := err.(*sftp.StatusError) //nolint:errorlint // ce maillon seul, jamais la chaîne
	if err == nil || isPathError || isServerText {
		return false
	}
	if isSendFailureElement(err) {
		return true
	}
	switch wrapper := err.(type) { //nolint:errorlint // parcours maillon par maillon, voulu
	case interface{ Unwrap() []error }:
		for _, joined := range wrapper.Unwrap() {
			if hasSendFailure(joined) {
				return true
			}
		}
	case interface{ Unwrap() error }:
		return hasSendFailure(wrapper.Unwrap())
	}
	return false
}

// isSendFailureElement : ce maillon seul commence par le texte d'envoi et porte une erreur de transport.
func isSendFailureElement(err error) bool {
	inner := errors.Unwrap(err)
	if inner == nil || !strings.HasPrefix(err.Error(), sendFailurePrefix) {
		return false
	}
	var networkError net.Error
	return errors.Is(inner, io.EOF) || errors.Is(inner, io.ErrClosedPipe) || errors.Is(inner, net.ErrClosed) ||
		errors.As(inner, &networkError)
}
