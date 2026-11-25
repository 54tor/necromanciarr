package sftpfs

import (
	"errors"
	"io"
	"net"
	"slices"
	"strings"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// transportMessages : textes des échecs d'envoi de pkg/sftp v1.13.13 (packet.go, request-errors.go), qui
// enveloppent parfois io.EOF sans type propre.
var transportMessages = []string{"failed to send packet", "connection lost"}

// isTransportFailure reconnaît une connexion perdue par la chaîne ou le texte de l'erreur, sans dépendre du moment
// où la fin de session est remarquée : un io.EOF d'envoi n'est jamais une fin de fichier.
func isTransportFailure(err error) bool {
	if err == nil {
		return false
	}
	var exitMissing *ssh.ExitMissingError
	isTyped := errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, net.ErrClosed) || errors.As(err, &exitMissing)
	if isTyped {
		return true
	}
	message := err.Error()
	return slices.ContainsFunc(transportMessages, func(transportMessage string) bool {
		return strings.Contains(message, transportMessage)
	})
}
