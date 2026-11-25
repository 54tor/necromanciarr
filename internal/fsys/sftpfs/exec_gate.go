package sftpfs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs/shellquote"
)

// Raisons de coupure de l'exécution distante (ExecDisabledError.Reason, Capabilities.Reasons["remoteExec"]) : codes
// stables, jamais traduits.
const (
	// ExecReasonOff : exec vaut off ; aucune commande n'est jamais envoyée.
	ExecReasonOff = "exec_off"
	// ExecReasonDenied : le serveur refuse exec (compte restreint, garde de commande) ; lecture SFTP seule.
	ExecReasonDenied = "exec_denied"
	// ExecReasonLoginShellNotPOSIX : la sonde du shell de connexion a fini sans rendre ShellSemanticsExpected (fish,
	// csh) : toute commande est coupée pour la session, lignes constantes comprises.
	ExecReasonLoginShellNotPOSIX = "login_shell_not_posix"
	// ExecReasonProbeFailed : une ligne de sonde n'a pas fini (délai, sortie démesurée, canal coupé), ou stat --version
	// est absent, en échec ou pas GNU coreutils.
	ExecReasonProbeFailed = "exec_probe_failed"
	// ExecReasonProgramMissing : sha1sum ou md5sum absent ou pas GNU coreutils ; stat reste permis.
	ExecReasonProgramMissing = "exec_program_missing"
)

// reasonKeyRemoteExec : clé de Capabilities.Reasons de l'exécution distante.
const reasonKeyRemoteExec = "remoteExec"

const (
	coreutilsMarker = "(GNU coreutils)"
	findutilsMarker = "(GNU findutils)"
)

// versionProbe est une ligne de sonde constante : le programme est présent quand la ligne finit à 0 et, si marker
// n'est pas vide, que la première ligne de sa sortie le contient.
type versionProbe struct {
	program, line, marker string
}

// versionProbes suit la sonde du shell de connexion ; stat --version est CommandProbe de la table.
var versionProbes = []versionProbe{
	{program: "stat", line: commandLines[CommandProbe], marker: coreutilsMarker},
	{program: "xargs", line: mustJoin([]string{"xargs", "--version"}), marker: findutilsMarker},
	{program: "sha1sum", line: mustJoin([]string{"sha1sum", "--version"}), marker: coreutilsMarker},
	{program: "md5sum", line: mustJoin([]string{"md5sum", "--version"}), marker: coreutilsMarker},
	{program: "nice", line: mustJoin([]string{"nice", "--version"})},
	{program: "ionice", line: mustJoin([]string{"ionice", "--version"})},
}

// ExecDisabledError dit pourquoi l'exécution distante est coupée (Reason : un code ExecReason*) ; errors.Is la
// reconnaît comme ErrExecDisabled.
type ExecDisabledError struct {
	Reason string
}

func (disabled *ExecDisabledError) Error() string {
	return "sftpfs: remote commands disabled: " + disabled.Reason
}

// Is rend vrai pour ErrExecDisabled.
func (disabled *ExecDisabledError) Is(target error) bool { return target == ErrExecDisabled }

// ExecStatus est le verdict de la sonde d'une session : Enabled quand le shell de connexion est POSIX et stat GNU ;
// Reason (ExecReason*) sinon. BatchMode : xargs -0 présent (lots NUL), sinon un fichier par commande. Hashes liste
// les empreintes disponibles (« sha1 », « md5 ») ; LowPriority : nice et ionice présents.
type ExecStatus struct {
	Enabled     bool
	Reason      string
	BatchMode   bool
	Hashes      []string
	LowPriority bool
}

// ProbeExec sonde, une fois par session, ce que l'hôte permet : d'abord shellquote.ShellSemanticsProbe, dont la
// sortie doit être ShellSemanticsExpected octet pour octet (sinon login_shell_not_posix et plus aucune ligne n'est
// envoyée de la session), puis des lignes constantes « --version ». Chaque ligne est bornée (30 s, 4 Kio). exec off :
// aucun envoi, raison exec_off. exec required et exécution indisponible : ErrExecRequiredUnavailable. Une session
// perdue rend fsys.ErrRemoteUnavailable, sans verdict gardé.
func (volume *Volume) ProbeExec(ctx context.Context) (ExecStatus, error) {
	if volume.provider.Exec == config.SFTPExecOff {
		return ExecStatus{Reason: ExecReasonOff}, nil
	}
	if err := volume.lockExec(ctx); err != nil {
		return ExecStatus{}, err
	}
	defer volume.unlockExec()
	current, err := volume.acquire(ctx)
	if err != nil {
		return ExecStatus{}, err
	}
	status, err := volume.probeExecOn(ctx, current)
	if err != nil {
		return ExecStatus{}, err
	}
	if !status.Enabled && volume.provider.Exec == config.SFTPExecRequired {
		return status, volume.disabledError(status.Reason)
	}
	return status, nil
}

// probeExecOn rend le verdict gardé par la session, ou sonde ; le droit d'exécuter est tenu. Un verdict passager (une
// ligne de sonde hors délai) reste gardé pour l'étape et tombe à ResetForStage.
func (volume *Volume) probeExecOn(ctx context.Context, current *session) (ExecStatus, error) {
	if cached := current.execStatus.Load(); cached != nil {
		return cloneStatus(*cached), nil
	}
	status, transient, err := volume.runExecProbe(ctx, current)
	if err != nil {
		return ExecStatus{}, err
	}
	current.execVerdictTransient.Store(transient)
	current.execStatus.Store(&status)
	return cloneStatus(status), nil
}

// forgetTransientExecVerdict oublie un verdict passager : la prochaine commande sonde à nouveau.
func (current *session) forgetTransientExecVerdict() {
	if current.execVerdictTransient.CompareAndSwap(true, false) {
		current.execStatus.Store(nil)
	}
}

// runExecProbe rend le verdict et s'il est passager (une ligne hors délai : aucun verdict sur les programmes, refait à
// l'étape suivante, exec coupé d'ici là).
func (volume *Volume) runExecProbe(ctx context.Context, current *session) (ExecStatus, bool, error) {
	output, err := volume.runLine(ctx, current, shellquote.ShellSemanticsProbe, nil, maxProbeOutputBytes, probeDeadline())
	if isConclusiveExecError(ctx, err) {
		return ExecStatus{}, false, err
	}
	if reason := shellReason(output, err); reason != "" {
		return ExecStatus{Reason: reason}, errors.Is(err, ErrExecTimeout), nil
	}
	found := map[string]bool{}
	for _, probe := range versionProbes {
		output, err := volume.runLine(ctx, current, probe.line, nil, maxProbeOutputBytes, probeDeadline())
		if isConclusiveExecError(ctx, err) {
			return ExecStatus{}, false, err
		}
		if errors.Is(err, ErrExecTimeout) {
			return ExecStatus{Reason: ExecReasonProbeFailed}, true, nil
		}
		found[probe.program] = err == nil && probe.matches(output)
	}
	if !found["stat"] {
		return ExecStatus{Reason: ExecReasonProbeFailed}, false, nil
	}
	status := ExecStatus{Enabled: true, BatchMode: found["xargs"], LowPriority: found["nice"] && found["ionice"]}
	for _, command := range []CommandID{CommandSHA1, CommandMD5} {
		algorithm := hashAlgorithms[command]
		if found[algorithm.name+"sum"] {
			status.Hashes = append(status.Hashes, algorithm.name)
		}
	}
	return status, false, nil
}

// shellReason range la sonde du shell de connexion : refus d'exec ; exécution complète (code de sortie reçu) dont la
// sortie n'est pas celle d'un shell POSIX ; ou exécution incomplète (délai, sortie démesurée, canal coupé), qui ne dit
// rien du shell. Vide quand le shell est POSIX.
func shellReason(output []byte, probeErr error) string {
	isCompletedRun := probeErr == nil || errors.Is(probeErr, errExecExitStatus)
	switch {
	case errors.Is(probeErr, errExecRefused):
		return ExecReasonDenied
	case !isCompletedRun:
		return ExecReasonProbeFailed
	case probeErr != nil || string(output) != shellquote.ShellSemanticsExpected:
		return ExecReasonLoginShellNotPOSIX
	}
	return ""
}

func probeDeadline() time.Time { return time.Now().Add(statExecTimeout) }

func (probe versionProbe) matches(output []byte) bool {
	firstLine, _, _ := strings.Cut(string(output), "\n")
	return probe.marker == "" || strings.Contains(firstLine, probe.marker)
}

// isConclusiveExecError : une erreur qui empêche tout verdict (session perdue, ctx) plutôt qu'un refus de l'hôte.
func isConclusiveExecError(ctx context.Context, err error) bool {
	return err != nil && (errors.Is(err, fsys.ErrRemoteUnavailable) || ctx.Err() != nil)
}

// disabledError rend la raison typée ; avec exec required, elle est aussi ErrExecRequiredUnavailable.
func (volume *Volume) disabledError(reason string) error {
	disabled := &ExecDisabledError{Reason: reason}
	if volume.provider.Exec == config.SFTPExecRequired {
		return fmt.Errorf("%w: %w", ErrExecRequiredUnavailable, disabled)
	}
	return disabled
}

func cloneStatus(status ExecStatus) ExecStatus {
	status.Hashes = slices.Clone(status.Hashes)
	return status
}

// execReason rend la raison de coupure connue pour la session (exec off, ou verdict de sonde), vide sinon.
func (volume *Volume) execReason(current *session) string {
	if volume.provider.Exec == config.SFTPExecOff {
		return ExecReasonOff
	}
	if status := current.execStatus.Load(); status != nil {
		return status.Reason
	}
	return ""
}

// withExecReason ajoute à Reasons la raison de coupure de l'exécution distante, quand elle est connue.
func (volume *Volume) withExecReason(current *session, capabilities fsys.Capabilities) fsys.Capabilities {
	reason := volume.execReason(current)
	if reason == "" {
		return capabilities
	}
	if capabilities.Reasons == nil {
		capabilities.Reasons = map[string]string{}
	}
	capabilities.Reasons[reasonKeyRemoteExec] = reason
	return capabilities
}

// guardLines rend, dans l'ordre d'envoi, chaque ligne constante qu'une garde de commande distante doit autoriser
// (« volumes commands ») : sonde du shell, sondes de version, lignes en lots, lignes d'empreinte préfixées.
func guardLines() []string {
	lines := []string{shellquote.ShellSemanticsProbe}
	for _, probe := range versionProbes {
		lines = append(lines, probe.line)
	}
	for _, command := range []CommandID{CommandStat, CommandSHA1, CommandMD5} {
		lines = append(lines, commandLines[command])
	}
	return append(lines, lowPriorityLines[CommandSHA1], lowPriorityLines[CommandMD5])
}
