package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
)

// sftpTrustCommand décrit la ligne de sftp trust : le nom du fournisseur, puis --yes. Commande de la CLI seule, sans
// action du registre ni route web (spec 4.C.5, Q13) : elle n'ouvre ni base, ni registre, ni serveur.
var sftpTrustCommand = command{
	path: []string{"sftp", "trust"}, arguments: []string{app.ArgumentVolumeName}, boolFlags: []string{flagYes},
}

var (
	errTrustUnknownProvider = errors.New("sftp trust: unknown provider")
	errTrustNotSFTP         = errors.New("sftp trust: provider is not of type sftp")
)

// runSFTPTrust affiche l'empreinte SHA-256 de la clé d'hôte présentée, sans jamais s'authentifier. Sans --yes, rien
// n'est écrit et la sortie vaut 2 ; avec --yes, la ligne est ajoutée au knownHosts du fournisseur. Une autre clé déjà
// épinglée pour l'hôte est refusée (host_key_mismatch) : l'opérateur corrige le fichier lui-même.
func runSFTPTrust(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	call, err := parseInvocation(sftpTrustCommand, args)
	if err != nil {
		return reportError(stderr, args, err)
	}
	settings, err := config.Load(call.configArgs, lookupEnv, defaultConfigDir())
	if err != nil {
		return reportError(stderr, args, err)
	}
	out := newPrinter(stdout, stderr, args)
	out.useSettings(settings)
	name := call.arguments[app.ArgumentVolumeName]
	provider, err := sftpProviderNamed(settings, name)
	if err != nil {
		return out.trustRefusal(err, name)
	}
	hostKey, err := sftpfs.FetchHostKey(ctx, provider)
	if err != nil {
		return out.trustFailure(err)
	}
	out.line(out.translator.Text(i18n.KeyCLISFTPTrustFingerprint, textParams("volume", name, "fingerprint", hostKey.Fingerprint())))
	out.warn([]string{out.translator.Text(i18n.KeyCLISFTPTrustVerify, nil)})
	outcome, err := sftpfs.TrustHostKey(provider, hostKey, call.has(flagYes))
	if err != nil {
		return out.trustFailure(err)
	}
	return out.trustOutcome(outcome, provider.KnownHosts)
}

func sftpProviderNamed(settings config.Config, name string) (config.FSProvider, error) {
	for _, provider := range settings.FSProviders {
		if provider.Name != name {
			continue
		}
		if provider.Type != config.FSProviderTypeSFTP {
			return config.FSProvider{}, errTrustNotSFTP
		}
		return provider, nil
	}
	return config.FSProvider{}, errTrustUnknownProvider
}

func (out printer) line(text string) {
	_, _ = fmt.Fprintln(out.stdout, out.redact(text))
}

func (out printer) trustRefusal(err error, name string) int {
	key := i18n.KeyCLISFTPTrustUnknownProvider
	if errors.Is(err, errTrustNotSFTP) {
		key = i18n.KeyCLISFTPTrustNotSFTP
	}
	_, _ = fmt.Fprintln(out.stderr, "error:", out.redact(out.translator.Text(key, textParams("volume", name))))
	return failureExitCode
}

// trustFailure n'affiche que le code stable et son libellé, jamais le texte de l'erreur.
func (out printer) trustFailure(err error) int {
	code := sftpfs.Code(err)
	if code == "" || errors.Is(err, fsys.ErrRemoteUnavailable) {
		code = pipeline.ReasonRemoteUnavailable
	}
	message := out.translator.Text(i18n.KeyCLISFTPTrustFailed, textParams("code", code, "label", out.translator.ReasonLabel(code)))
	_, _ = fmt.Fprintln(out.stderr, "error:", out.redact(message))
	return failureExitCode
}

func (out printer) trustOutcome(outcome sftpfs.TrustOutcome, knownHosts string) int {
	params := textParams("known_hosts", knownHosts)
	switch outcome {
	case sftpfs.TrustAdded:
		out.line(out.translator.Text(i18n.KeyCLISFTPTrustAdded, params))
	case sftpfs.TrustAlreadyPinned:
		out.line(out.translator.Text(i18n.KeyCLISFTPTrustAlreadyPinned, params))
	default:
		out.line(out.translator.Text(i18n.KeyCLISFTPTrustNeedsYes, params))
		return usageExitCode
	}
	return successExitCode
}
