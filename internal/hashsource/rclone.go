package hashsource

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultRcloneBinary = "rclone"
	defaultTimeout      = 60 * time.Second
	// maxExecOutputBytes borne la sortie lue : une ligne par fichier demandé, donc quelques centaines d'octets.
	maxExecOutputBytes = 64 << 10
	// killWaitDelay évite qu'un petit-fils qui garde le tube ouvert bloque Wait après le kill.
	killWaitDelay       = time.Second
	hashFieldSeparator  = "  "
	unsupportedHashMark = "UNSUPPORTED"
)

// ExecOptions règle l'adaptateur qui lance le binaire rclone ; Timeout vaut 60 s par défaut.
type ExecOptions struct {
	Binary  string
	Mounts  []Mount
	Timeout time.Duration
}

type rcloneExec struct {
	binary  string
	mounts  mountTable
	timeout time.Duration
}

// NewRcloneExec renvoie la source qui interroge `rclone hashsum` ; jamais `--download`, donc aucune lecture du
// contenu. rclone utilise sa propre configuration : elle n'est ni lue ni transmise ici.
func NewRcloneExec(options ExecOptions) (HashSource, error) {
	table, err := newMountTable(options.Mounts)
	if err != nil {
		return nil, err
	}
	binary, timeout := options.Binary, options.Timeout
	if binary == "" {
		binary = defaultRcloneBinary
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &rcloneExec{binary: binary, mounts: table, timeout: timeout}, nil
}

// remoteFor assemble « remote:chemin » ; un remote qui finit par « : » n'a pas de barre de séparation.
func remoteFor(mount Mount, relative string) string {
	if strings.HasSuffix(mount.Remote, ":") || strings.HasSuffix(mount.Remote, "/") {
		return mount.Remote + relative
	}
	return mount.Remote + "/" + relative
}

// Hash demande l'empreinte au backend ; une sortie vide dit que le backend n'en fournit pas.
func (source *rcloneExec) Hash(ctx context.Context, path string, algorithm Algorithm) (Result, error) {
	if err := checkAlgorithm(algorithm); err != nil {
		return Result{}, err
	}
	mount, relative, err := source.mounts.resolve(path)
	if err != nil {
		return Result{}, err
	}
	// « -- » : ce qui suit n'est jamais pris pour une option, même un nom de fichier qui commence par « - ».
	arguments := []string{"hashsum", string(algorithm), "--", remoteFor(mount, relative)}
	output, err := source.run(ctx, arguments)
	if err != nil {
		return Result{}, err
	}
	return parseHashsum(output, algorithm, filepath.Base(path))
}

func (source *rcloneExec) run(ctx context.Context, arguments []string) ([]byte, error) {
	callContext, cancel := context.WithTimeout(ctx, source.timeout)
	defer cancel()
	output := &cappedBuffer{limit: maxExecOutputBytes, onExceed: cancel}
	command := exec.CommandContext(callContext, source.binary, arguments...) // #nosec G204 -- binaire de la configuration, arguments séparés issus d'une liste fermée et de chemins validés, jamais de shell
	command.Stdout = output
	command.WaitDelay = killWaitDelay
	err := command.Run()
	switch {
	case output.exceeded:
		return nil, fmt.Errorf("%w: output too large", ErrRcloneFailed)
	case ctx.Err() != nil:
		return nil, ctx.Err()
	case errors.Is(callContext.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("%w: timeout after %s", ErrRcloneFailed, source.timeout)
	case err != nil:
		return nil, fmt.Errorf("%w: %s", ErrRcloneFailed, describeExit(err))
	}
	return output.buffer.Bytes(), nil
}

// describeExit ne cite que le code de sortie : stdout et stderr peuvent contenir des identifiants.
func describeExit(err error) string {
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return fmt.Sprintf("exit code %d", exitError.ExitCode())
	}
	return "could not run the binary"
}

var errOutputTooLarge = errors.New("output too large")

// cappedBuffer refuse tout octet au-delà de limit et prévient l'appelant, qui tue alors le processus.
type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	onExceed func()
}

func (capped *cappedBuffer) Write(chunk []byte) (int, error) {
	if capped.buffer.Len()+len(chunk) > capped.limit {
		capped.exceeded = true
		capped.onExceed()
		return 0, errOutputTooLarge
	}
	return capped.buffer.Write(chunk)
}

// parseHashsum lit des lignes « <empreinte>  <nom> ». Toute ligne mal formée rend la sortie inexploitable ; seule
// l'empreinte du fichier demandé est retenue, les lignes d'autres noms sont ignorées.
func parseHashsum(output []byte, algorithm Algorithm, fileName string) (Result, error) {
	var found Result
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			continue
		}
		value, name, isWellFormed := strings.Cut(line, hashFieldSeparator)
		if !isWellFormed || !isAcceptableHash(value, algorithm) {
			return Result{}, fmt.Errorf("%w: unexpected output", ErrRcloneFailed)
		}
		if name != fileName || value == "" || value == unsupportedHashMark {
			continue
		}
		value = strings.ToLower(value)
		if found.Available && found.Value != value {
			return Result{}, fmt.Errorf("%w: conflicting output", ErrRcloneFailed)
		}
		found = Result{Value: value, Origin: Backend, Available: true}
	}
	return found, nil
}

// isAcceptableHash : vide ou « UNSUPPORTED » (le backend n'a pas cette empreinte) ou hexadécimal de la bonne longueur.
func isAcceptableHash(value string, algorithm Algorithm) bool {
	if value == "" || value == unsupportedHashMark {
		return true
	}
	_, err := hex.DecodeString(value)
	return err == nil && len(value) == algorithm.hexLength()
}
