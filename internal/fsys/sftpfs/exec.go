package sftpfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/sftpfs/shellquote"
)

// CommandID identifie une commande distante autorisée ; il n'existe aucun moyen d'en construire une autre.
type CommandID int

const (
	// CommandProbe sonde l'hôte (shell de connexion POSIX, stat, xargs, sha1sum, md5sum, nice, ionice) ; sans chemin.
	CommandProbe CommandID = iota
	// CommandStat rend périphérique, inode, nombre de liens, taille et date de chaque chemin.
	CommandStat
	// CommandSHA1 rend l'empreinte SHA-1 de chaque chemin, calculée par l'hôte distant.
	CommandSHA1
	// CommandMD5 rend l'empreinte MD5 de chaque chemin, calculée par l'hôte distant.
	CommandMD5
)

// commands est l'unique table d'exécution distante : constantes, sans aucune donnée externe.
var commands = map[CommandID][]string{
	CommandProbe: {"stat", "--version"},
	CommandStat:  {"xargs", "-0", "-r", "stat", "--printf=%d\t%i\t%h\t%s\t%Y\\0", "--"},
	CommandSHA1:  {"xargs", "-0", "-r", "sha1sum", "-z", "--"},
	CommandMD5:   {"xargs", "-0", "-r", "md5sum", "-z", "--"},
}

// lowPriorityPrefix précède les empreintes quand la sonde a trouvé nice et ionice : le calcul ne doit pas écraser le
// disque de l'hôte distant.
var lowPriorityPrefix = []string{"nice", "-n", "10", "ionice", "-c3"}

// Lignes envoyées, construites une fois par shellquote.Join à partir des seules constantes ci-dessus.
var (
	commandLines     = joinTable(commands)
	lowPriorityLines = map[CommandID]string{
		CommandSHA1: mustJoin(slices.Concat(lowPriorityPrefix, commands[CommandSHA1])),
		CommandMD5:  mustJoin(slices.Concat(lowPriorityPrefix, commands[CommandMD5])),
	}
)

// hashAlgorithm : nom rendu dans ExecStatus.Hashes et longueur hexadécimale de l'empreinte.
type hashAlgorithm struct {
	name      string
	hexLength int
}

var hashAlgorithms = map[CommandID]hashAlgorithm{
	CommandSHA1: {name: "sha1", hexLength: 40},
	CommandMD5:  {name: "md5", hexLength: 32},
}

const (
	// maxExecBatch : chemins par commande ; au-delà, Exec découpe en lots.
	maxExecBatch = 64
	// maxStatOutputBytes borne la sortie d'un lot stat ; hashRecordOverhead s'ajoute à chaque chemin d'un lot
	// d'empreintes.
	maxStatOutputBytes = 64 << 10
	hashRecordOverhead = 128
	// maxStderrBytes : sortie d'erreur lue au plus, jamais recopiée (elle peut citer des chemins).
	maxStderrBytes = 4 << 10
	// maxProbeOutputBytes borne la sortie d'une ligne de sonde.
	maxProbeOutputBytes = 4 << 10
	// defaultExecHashTimeout s'applique quand execHashTimeout n'est pas réglé.
	defaultExecHashTimeout = 30 * time.Minute
	// xargsPrefixLength : « xargs -0 -r » ouvre chaque ligne en lots ; le mode un fichier par commande les retire.
	xargsPrefixLength = 3
	// liveCheckWait borne l'attente d'un keepalive qui dit si la connexion vit encore après un échec d'exec.
	liveCheckWait = 5 * time.Second
	// abortGrace : délai laissé à une session tuée pour se terminer avant que la connexion entière soit fermée.
	abortGrace = 5 * time.Second
)

// statExecTimeout borne un lot stat et chaque ligne de sonde (variable : les tests l'abaissent).
var statExecTimeout = 30 * time.Second

// Erreurs d'exécution distante, à tester avec errors.Is. Aucune ne cite la sortie d'erreur distante, ni un chemin
// refusé : seulement un rang, un code de sortie ou une raison typée.
var (
	// ErrExecDisabled : l'exécution distante est coupée sur ce volume ou cette session (ExecDisabledError dit
	// pourquoi) ; l'appelant lit en SFTP seul.
	ErrExecDisabled = errors.New("sftpfs: remote commands disabled")
	// ErrExecRequiredUnavailable : exec vaut required et la sonde n'a pas rendu l'exécution distante utilisable.
	ErrExecRequiredUnavailable = fmt.Errorf("sftpfs: %w", fsys.ErrRemoteExecRequired)
	// ErrExecPathOutsideRoots : un chemin n'est pas absolu, canonique, sans NUL, d'au plus 4096 octets, ou n'est sous
	// aucune racine de bibliothèque, dossier de liens ou racine torrent du volume ; rien n'a été envoyé.
	ErrExecPathOutsideRoots = errors.New("sftpfs: exec path is not a canonical path under a root of this volume")
	// ErrExecOutputTooLarge : une sortie a dépassé sa borne ; la session a été tuée et le résultat rejeté.
	ErrExecOutputTooLarge = errors.New("sftpfs: remote command output too large")
	// ErrExecTimeout : une commande a dépassé son délai ; la session a reçu KILL puis a été fermée.
	ErrExecTimeout = errors.New("sftpfs: remote command timed out")
	// ErrExecFailed : la commande a été refusée, a fini sans code de sortie ou avec un code non nul.
	ErrExecFailed = errors.New("sftpfs: remote command failed")
	// ErrExecMalformedOutput : un enregistrement manque, est en trop, mal formé ou ne correspond pas au chemin
	// envoyé ; tout le lot est rejeté.
	ErrExecMalformedOutput = errors.New("sftpfs: remote command output does not match the paths sent")
	// ErrExecSizeMismatch : la taille rendue par stat diffère de celle que donne SFTP pour ce chemin (ExecResult.Err).
	ErrExecSizeMismatch = errors.New("sftpfs: remote stat size differs from the sftp size")
)

// errExecRefused : le serveur a refusé le canal ou la requête exec ; errExecIncomplete : la commande a fini sans code
// de sortie (canal coupé). Le texte de x/crypto, qui cite la ligne, n'est jamais gardé.
var (
	errExecRefused    = fmt.Errorf("%w: the server refused the command", ErrExecFailed)
	errExecIncomplete = fmt.Errorf("%w: no exit status", ErrExecFailed)
	// errExecExitStatus : la commande a fini avec un code non nul (exécution complète).
	errExecExitStatus = fmt.Errorf("%w: exit status", ErrExecFailed)
	errOutputCapped   = errors.New("sftpfs: output capped")
)

// ExecResult est le résultat d'une commande pour un chemin : Device, Inode, Links, Size et ModTime pour stat, Hash
// (hexadécimal minuscule) pour une empreinte. Err est ErrExecSizeMismatch, ou l'erreur du Lstat SFTP de contrôle.
type ExecResult struct {
	Path                 string
	Device, Inode, Links uint64
	Size                 int64
	ModTime              time.Time
	Hash                 string
	Err                  error
}

// Exec lance une commande autorisée ; les chemins passent par l'entrée standard, séparés par NUL, par lots de 64, une
// session SSH par lot, une commande à la fois par volume. Les résultats suivent l'ordre de paths. Chaque chemin est
// admis avant tout envoi (ErrExecPathOutsideRoots), par comparaison de texte : l'appelant passe des chemins rendus par
// Canonical (RealPath du serveur), jamais un chemin configuré ou reçu tel quel ; le premier usage d'une session la sonde (ProbeExec). La taille
// de chaque stat est comparée à un Lstat SFTP. Un lot en échec fait échouer l'appel entier : jamais de résultat
// incomplet. CommandProbe ne prend aucun chemin : il sonde la session et rend ErrExecDisabled si l'exécution distante y
// est coupée.
func (volume *Volume) Exec(ctx context.Context, command CommandID, paths []string) ([]ExecResult, error) {
	if _, known := commands[command]; !known {
		return nil, fmt.Errorf("sftpfs: exec: command %d: %w", command, fs.ErrInvalid)
	}
	if command == CommandProbe {
		if len(paths) > 0 {
			return nil, fmt.Errorf("sftpfs: exec: the probe takes no path: %w", fs.ErrInvalid)
		}
		status, err := volume.ProbeExec(ctx)
		if err == nil && !status.Enabled {
			err = volume.disabledError(status.Reason)
		}
		return nil, err
	}
	if err := volume.admitPaths(paths); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return []ExecResult{}, nil
	}
	if err := volume.lockExec(ctx); err != nil {
		return nil, err
	}
	defer volume.unlockExec()
	current, status, err := volume.enabledSession(ctx, command)
	if err != nil {
		return nil, err
	}
	results, err := volume.runBatches(ctx, current, status, command, paths)
	if err != nil {
		return nil, err
	}
	if command == CommandStat {
		return volume.checkSizes(ctx, results)
	}
	return results, nil
}

// lockExec prend le droit d'exécuter (une commande à la fois par volume) ; un appelant en attente derrière un long lot
// d'empreintes renonce à l'annulation de ctx.
func (volume *Volume) lockExec(ctx context.Context) error {
	select {
	case volume.execSlot <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sftpfs: exec on %s: %w", volume.provider.Address, ctx.Err())
	}
}

func (volume *Volume) unlockExec() { <-volume.execSlot }

// enabledSession rend la session et sa sonde quand la commande y est autorisée.
func (volume *Volume) enabledSession(ctx context.Context, command CommandID) (*session, ExecStatus, error) {
	if volume.provider.Exec == config.SFTPExecOff {
		return nil, ExecStatus{}, &ExecDisabledError{Reason: ExecReasonOff}
	}
	current, err := volume.acquire(ctx)
	if err != nil {
		return nil, ExecStatus{}, err
	}
	status, err := volume.probeExecOn(ctx, current)
	if err != nil {
		return nil, ExecStatus{}, err
	}
	if !status.Enabled {
		return nil, status, volume.disabledError(status.Reason)
	}
	if algorithm, isHash := hashAlgorithms[command]; isHash && !slices.Contains(status.Hashes, algorithm.name) {
		return nil, status, volume.disabledError(ExecReasonProgramMissing)
	}
	return current, status, nil
}

func (volume *Volume) runBatches(ctx context.Context, current *session, status ExecStatus, command CommandID, paths []string) ([]ExecResult, error) {
	results := make([]ExecResult, 0, len(paths))
	for batchStart := 0; batchStart < len(paths); batchStart += maxExecBatch {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch := paths[batchStart:min(batchStart+maxExecBatch, len(paths))]
		batchResults, err := volume.runBatch(ctx, current, status, command, batch)
		if err != nil {
			return nil, err
		}
		results = append(results, batchResults...)
	}
	return results, nil
}

// runBatch envoie la ligne constante et les chemins sur l'entrée standard ; sans xargs -0 (sonde), un fichier par
// commande, sous le même délai de lot.
func (volume *Volume) runBatch(ctx context.Context, current *session, status ExecStatus, command CommandID, paths []string) ([]ExecResult, error) {
	deadline := time.Now().Add(volume.batchTimeout(command))
	if !status.BatchMode {
		return volume.runPerFile(ctx, current, status, command, paths, deadline)
	}
	var stdin []byte
	for _, remotePath := range paths {
		stdin = append(append(stdin, remotePath...), 0)
	}
	stdout, err := volume.runLine(ctx, current, batchLine(command, status), stdin, outputLimit(command, paths), deadline)
	if err != nil {
		return nil, err
	}
	return parseRecords(command, paths, stdout)
}

// runPerFile est le repli « un fichier par commande » : le chemin, absolu et admis, est le seul argument externe,
// quoté par shellquote.Quote après « -- ».
func (volume *Volume) runPerFile(ctx context.Context, current *session, status ExecStatus, command CommandID, paths []string, deadline time.Time) ([]ExecResult, error) {
	results := make([]ExecResult, 0, len(paths))
	for _, remotePath := range paths {
		line, err := shellquote.Join(fallbackArgv(command, status, remotePath))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrExecPathOutsideRoots, err)
		}
		single := []string{remotePath}
		stdout, err := volume.runLine(ctx, current, line, nil, outputLimit(command, single), deadline)
		if err != nil {
			return nil, err
		}
		parsed, err := parseRecords(command, single, stdout)
		if err != nil {
			return nil, err
		}
		results = append(results, parsed...)
	}
	return results, nil
}

// checkSizes compare la taille de chaque stat à celle d'un Lstat SFTP du même chemin (stat ne suit pas le lien final).
func (volume *Volume) checkSizes(ctx context.Context, results []ExecResult) ([]ExecResult, error) {
	for resultIndex := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := &results[resultIndex]
		info, err := volume.Lstat(result.Path)
		switch {
		case errors.Is(err, fsys.ErrRemoteUnavailable):
			return nil, err
		case err != nil:
			result.Err = err
		case info.Size() != result.Size:
			result.Err = ErrExecSizeMismatch
		}
	}
	return results, nil
}

func (volume *Volume) batchTimeout(command CommandID) time.Duration {
	if _, isHash := hashAlgorithms[command]; !isHash {
		return statExecTimeout
	}
	if volume.provider.ExecHashTimeout > 0 {
		return volume.provider.ExecHashTimeout
	}
	return defaultExecHashTimeout
}

// outputLimit : 64 Kio pour stat, la somme des (longueur + 128) des chemins pour une empreinte.
func outputLimit(command CommandID, paths []string) int {
	if _, isHash := hashAlgorithms[command]; !isHash {
		return maxStatOutputBytes
	}
	limit := 0
	for _, remotePath := range paths {
		limit += len(remotePath) + hashRecordOverhead
	}
	return limit
}

func batchLine(command CommandID, status ExecStatus) string {
	if prefixed, hasPrefix := lowPriorityLines[command]; hasPrefix && status.LowPriority {
		return prefixed
	}
	return commandLines[command]
}

// fallbackArgv retire « xargs -0 -r » de l'argv constant, garde « -- » et ajoute le chemin.
func fallbackArgv(command CommandID, status ExecStatus, remotePath string) []string {
	argv := slices.Concat(commands[command][xargsPrefixLength:], []string{remotePath})
	if _, isHash := hashAlgorithms[command]; isHash && status.LowPriority {
		return slices.Concat(lowPriorityPrefix, argv)
	}
	return argv
}

// fallbackLine est la ligne « un fichier par commande » sans préfixe de priorité.
func fallbackLine(command CommandID, remotePath string) (string, error) {
	return shellquote.Join(fallbackArgv(command, ExecStatus{}, remotePath))
}

func joinTable(table map[CommandID][]string) map[CommandID]string {
	lines := make(map[CommandID]string, len(table))
	for id, argv := range table {
		lines[id] = mustJoin(argv)
	}
	return lines
}

// mustJoin quote un argv constant ; un NUL dans une constante est une faute de programmation.
func mustJoin(argv []string) string {
	line, err := shellquote.Join(argv)
	if err != nil {
		panic(err)
	}
	return line
}

// remoteCommand est une session exec en cours ; abort la tue (KILL puis fermeture du canal) une seule fois, avant ou
// après son ouverture.
type remoteCommand struct {
	mutex   sync.Mutex
	session *ssh.Session
	aborted bool
}

func (remote *remoteCommand) attach(opened *ssh.Session) bool {
	remote.mutex.Lock()
	defer remote.mutex.Unlock()
	if remote.aborted {
		return false
	}
	remote.session = opened
	return true
}

// abort envoie KILL (ignoré par OpenSSH avant 7.9) puis ferme le canal, ce qui débloque Start et Wait.
func (remote *remoteCommand) abort() {
	remote.mutex.Lock()
	if remote.aborted {
		remote.mutex.Unlock()
		return
	}
	remote.aborted = true
	opened := remote.session
	remote.mutex.Unlock()
	if opened != nil {
		_ = opened.Signal(ssh.SIGKILL)
		_ = opened.Close()
	}
}

// cappedBuffer garde au plus limit octets ; au-delà, il tue la session et refuse l'écriture.
type cappedBuffer struct {
	buffer     bytes.Buffer
	limit      int
	overflowed bool
	onOverflow func()
}

func (capped *cappedBuffer) Write(chunk []byte) (int, error) {
	if capped.buffer.Len()+len(chunk) > capped.limit {
		capped.overflowed = true
		capped.onOverflow()
		return 0, errOutputCapped
	}
	return capped.buffer.Write(chunk)
}

// run ouvre une session SSH à part des requêtes SFTP, y lance la ligne avec stdin, et attend la fin.
func (remote *remoteCommand) run(client *ssh.Client, line string, stdin []byte, stdoutLimit int) ([]byte, error) {
	opened, err := client.NewSession()
	if err != nil {
		return nil, errExecRefused
	}
	if !remote.attach(opened) {
		_ = opened.Close()
		return nil, errExecIncomplete
	}
	defer func() { _ = opened.Close() }()
	stdout := &cappedBuffer{limit: stdoutLimit, onOverflow: remote.abort}
	stderr := &cappedBuffer{limit: maxStderrBytes, onOverflow: remote.abort}
	opened.Stdin, opened.Stdout, opened.Stderr = bytes.NewReader(stdin), stdout, stderr
	if err := opened.Start(line); err != nil {
		return nil, errExecRefused
	}
	waitErr := opened.Wait()
	if stdout.overflowed || stderr.overflowed {
		return nil, ErrExecOutputTooLarge
	}
	if err := exitError(waitErr); err != nil {
		return nil, err
	}
	return stdout.buffer.Bytes(), nil
}

// exitError ne garde que le code de sortie : le message du serveur n'est jamais recopié.
func exitError(waitErr error) error {
	if waitErr == nil {
		return nil
	}
	var exited *ssh.ExitError
	if errors.As(waitErr, &exited) {
		return fmt.Errorf("%w %d", errExecExitStatus, exited.ExitStatus())
	}
	return errExecIncomplete
}

type lineOutcome struct {
	stdout []byte
	err    error
}

// runLine lance une ligne constante (ou de repli) jusqu'à deadline ; au délai ou à l'annulation de ctx, la session
// reçoit KILL puis est fermée, et runLine attend sa fin (la connexion entière est fermée si elle traîne). Un échec sur
// une connexion qui ne répond plus devient fsys.ErrRemoteUnavailable et casse le volume.
func (volume *Volume) runLine(ctx context.Context, current *session, line string, stdin []byte, stdoutLimit int, deadline time.Time) ([]byte, error) {
	if err := volume.sessionGone(current, "exec"); err != nil {
		return nil, err
	}
	remote := &remoteCommand{}
	outcome := make(chan lineOutcome, 1)
	go func() {
		stdout, err := remote.run(current.sshClient, line, stdin, stdoutLimit)
		outcome <- lineOutcome{stdout: stdout, err: err}
	}()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case result := <-outcome:
		return volume.lineResult(current, result)
	case <-timer.C:
		volume.abortRemote(current, remote, outcome)
		return nil, fmt.Errorf("sftpfs: exec on %s: %w", volume.provider.Address, ErrExecTimeout)
	case <-ctx.Done():
		volume.abortRemote(current, remote, outcome)
		return nil, fmt.Errorf("sftpfs: exec on %s: %w", volume.provider.Address, ctx.Err())
	}
}

// abortRemote tue la session et attend sa fin ; une session qui ne rend pas la main à temps fait fermer la connexion.
func (volume *Volume) abortRemote(current *session, remote *remoteCommand, outcome <-chan lineOutcome) {
	remote.abort()
	grace := time.NewTimer(abortGrace)
	defer grace.Stop()
	select {
	case <-outcome:
	case <-grace.C:
		volume.drop(current)
		<-outcome
	}
}

func (volume *Volume) lineResult(current *session, result lineOutcome) ([]byte, error) {
	if result.err == nil {
		return result.stdout, nil
	}
	isChannelFailure := errors.Is(result.err, errExecRefused) || errors.Is(result.err, errExecIncomplete)
	if isChannelFailure && !current.isAlive() {
		volume.drop(current)
		return nil, fmt.Errorf("%w: sftpfs: exec on %s: connection lost", fsys.ErrRemoteUnavailable, volume.provider.Address)
	}
	return nil, result.err
}

// isAlive : la connexion n'est ni fermée ni tombée, et répond à un keepalive.
func (current *session) isAlive() bool {
	select {
	case <-current.done:
		return false
	default:
	}
	return !current.closed.Load() && current.answersKeepalive(liveCheckWait)
}
