package sftpfs

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"necromanciarr/internal/config"
	"necromanciarr/internal/fsys"
)

// États d'un volume rendus par State.
const (
	// StateOK : une session vivante.
	StateOK = "ok"
	// StateBroken : aucune session ; la prochaine opération retente une fois si l'étape n'a pas déjà reconnecté.
	StateBroken = "broken"
	// StateUnknown : session perdue, nouvelle étape commencée, pas encore de nouvel essai.
	StateUnknown = "unknown"
)

const (
	keepaliveRequest = "keepalive@openssh.com"
	// maxMissedKeepalives : trois keepalive sans réponse et la session est morte.
	maxMissedKeepalives = 3
	probePath           = "."
)

// Deps porte ce que Dial ne lit pas dans la configuration. Limiter est le plafond de lecture global, gardé pour les
// lectures ; Now l'horloge ; LookupEnv lit SSH_AUTH_SOCK (nil : os.LookupEnv). LibraryRoots liste les racines de
// bibliothèque de ce volume (chemins POSIX canoniques) : ProbeCapabilities refuse d'écrire dans l'une d'elles, en
// plus du contrôle du registre.
type Deps struct {
	Limiter      fsys.Limiter
	Now          func() time.Time
	LookupEnv    func(string) (string, bool)
	LibraryRoots []string
}

func (deps Deps) withDefaults() Deps {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.LookupEnv == nil {
		deps.LookupEnv = os.LookupEnv
	}
	return deps
}

// session est une connexion SSH et son client SFTP ; done se ferme quand la connexion est tombée, lifetime est
// annulé à sa fermeture (attentes des limiteurs comprises).
type session struct {
	sshClient *ssh.Client
	client    *sftp.Client
	done      chan struct{}
	lifetime  context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	closed    atomic.Bool
	// symlinkOrder : verdict de la sonde d'ordre des arguments de SSH_FXP_SYMLINK sur cette session (symlinkOrder*).
	symlinkOrder atomic.Int32
	// capabilities : résultat de ProbeCapabilities sur cette session, nil tant qu'aucune sonde n'a abouti.
	capabilities atomic.Pointer[fsys.Capabilities]
	// execStatus : verdict de ProbeExec sur cette session, nil tant qu'aucune sonde de l'exécution distante n'a abouti.
	execStatus atomic.Pointer[ExecStatus]
}

func newSession(sshClient *ssh.Client, client *sftp.Client) *session {
	lifetime, cancel := context.WithCancel(context.Background())
	return &session{sshClient: sshClient, client: client, done: make(chan struct{}), lifetime: lifetime, cancel: cancel}
}

// close ferme la connexion SSH avant le client SFTP : c'est elle qui débloque les requêtes en vol.
func (current *session) close() {
	current.closeOnce.Do(func() {
		current.closed.Store(true)
		current.cancel()
		_ = current.sshClient.Close()
		_ = current.client.Close()
	})
}

// Volume est un fournisseur sftp joint, sûr pour des appels concurrents. Une session au plus ; perdue (délai,
// keepalive, coupure), elle est rouverte à l'opération suivante, au plus une fois par étape (ResetForStage).
type Volume struct {
	provider config.FSProvider
	deps     Deps
	// providerLimiter plafonne les lectures de ce fournisseur (readBytesPerSecond) ; nil : seul deps.Limiter.
	providerLimiter fsys.Limiter

	// probe sérialise les sondes et les nettoyages de fichiers de sonde.
	probeMutex sync.Mutex
	// execMutex : une commande distante (ou sonde) à la fois par volume.
	execMutex sync.Mutex
	// execRoots : racines admises par Exec en plus de Deps.LibraryRoots (AllowExecRoots, racines d'écriture).
	execRootsMutex sync.Mutex
	execRoots      []string
	// redial sérialise les reconnexions ; mutex protège les champs qui suivent.
	redial        sync.Mutex
	mutex         sync.Mutex
	current       *session
	state         string
	reconnectUsed bool
	closed        bool
	warnings      []Warning
}

// Dial ouvre la session du fournisseur. Échecs : ErrHostKeyUnknown, ErrHostKeyMismatch, ErrAuthRefused, erreurs
// de identityFile, ou fsys.ErrRemoteUnavailable (réseau, délai, annulation).
func Dial(ctx context.Context, provider config.FSProvider, deps Deps) (*Volume, error) {
	deps = deps.withDefaults()
	current, warnings, err := connect(ctx, provider, deps)
	if err != nil {
		return nil, err
	}
	volume := &Volume{
		provider: provider, deps: deps, providerLimiter: newProviderLimiter(provider),
		current: current, state: StateOK, warnings: warnings,
	}
	volume.watch(current)
	return volume, nil
}

// NewLazy rend le volume du fournisseur sans le joindre : la session s'ouvre à la première opération, qui consomme la
// reconnexion de l'étape. Un serveur injoignable au lancement n'empêche donc rien d'autre ; ses opérations rendent
// fsys.ErrRemoteUnavailable (cause et code de connexion gardés pour errors.Is et Code) jusqu'à l'étape suivante.
func NewLazy(provider config.FSProvider, deps Deps) *Volume {
	return &Volume{
		provider: provider, deps: deps.withDefaults(), providerLimiter: newProviderLimiter(provider), state: StateUnknown,
	}
}

// State rend ok, broken ou unknown.
func (volume *Volume) State() string {
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	return volume.state
}

// Warnings rend les avertissements de la dernière connexion (identity_file_permissions, agent_unavailable) et des
// racines d'écriture ouvertes depuis (link_dir_world_writable : Path est la racine, entre guillemets, Count le
// nombre de dossiers créés modifiables par d'autres).
func (volume *Volume) Warnings() []Warning {
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	return append([]Warning(nil), volume.warnings...)
}

// countWarning compte un constat de plus sous (code, path) : un seul avertissement par racine, quel que soit le
// nombre de dossiers en cause. Une reconnexion remplace la liste.
func (volume *Volume) countWarning(code, path string) {
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	for warningIndex := range volume.warnings {
		known := &volume.warnings[warningIndex]
		if known.Code == code && known.Path == path {
			known.Count++
			return
		}
	}
	volume.warnings = append(volume.warnings, Warning{Code: code, Path: path, Count: 1})
}

// ResetForStage ouvre une nouvelle étape : une reconnexion redevient autorisée.
func (volume *Volume) ResetForStage() {
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	volume.reconnectUsed = false
	if volume.current == nil && !volume.closed {
		volume.state = StateUnknown
	}
}

// Close ferme la session ; le volume ne se reconnecte plus jamais.
func (volume *Volume) Close() error {
	volume.mutex.Lock()
	current := volume.current
	volume.current, volume.state, volume.closed = nil, StateBroken, true
	volume.mutex.Unlock()
	if current != nil {
		current.close()
	}
	return nil
}

// Probe vérifie que la session répond, par un Stat du dossier de session, sous timeout.
func (volume *Volume) Probe(ctx context.Context) error {
	return volume.call(ctx, "stat", func(client *sftp.Client) error {
		_, err := client.Stat(probePath)
		return err
	})
}

// call exécute une opération SFTP sous timeout : c'est la seule voie vers le client SFTP. pkg/sftp n'accepte pas de
// contexte ; au délai ou à l'annulation, la connexion entière est fermée, ce qui débloque fn, et le volume passe
// broken. Un volume broken sans reconnexion autorisée rend fsys.ErrRemoteUnavailable sans appel réseau.
func (volume *Volume) call(ctx context.Context, operation string, fn func(client *sftp.Client) error) error {
	return volume.callSession(ctx, operation, func(current *session) error { return fn(current.client) })
}

// callSession est call pour une opération qui garde la session (un fichier ouvert y est lié).
func (volume *Volume) callSession(ctx context.Context, operation string, fn func(current *session) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := volume.acquire(ctx)
	if err != nil {
		return err
	}
	return volume.run(ctx, current, operation, fn)
}

// callOn exécute fn sur une session donnée, sans jamais rouvrir : un fichier d'une session perdue ne casse pas la
// session suivante.
func (volume *Volume) callOn(ctx context.Context, current *session, operation string, fn func(current *session) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := volume.sessionGone(current, operation); err != nil {
		return err
	}
	return volume.run(ctx, current, operation, fn)
}

// sessionGone rend fsys.ErrRemoteUnavailable pour une session fermée, sans appel réseau.
func (volume *Volume) sessionGone(current *session, operation string) error {
	if !current.closed.Load() {
		return nil
	}
	return fmt.Errorf("%w: sftpfs: %s on %s: session closed", fsys.ErrRemoteUnavailable, operation, volume.provider.Address)
}

func (volume *Volume) run(ctx context.Context, current *session, operation string, fn func(current *session) error) error {
	outcome := make(chan error, 1)
	go func() { outcome <- fn(current) }()
	timer := time.NewTimer(volume.provider.Timeout)
	defer timer.Stop()
	select {
	case err := <-outcome:
		return volume.operationError(current, operation, err)
	case <-timer.C:
		volume.drop(current)
		return fmt.Errorf("%w: sftpfs: %s on %s: %w", fsys.ErrRemoteUnavailable, operation, volume.provider.Address, ErrOperationTimeout)
	case <-ctx.Done():
		volume.drop(current)
		return fmt.Errorf("sftpfs: %s on %s: %w", operation, volume.provider.Address, ctx.Err())
	}
}

// operationError rend l'erreur de fn ; une connexion perdue ou fermée (Close, délai) devient fsys.ErrRemoteUnavailable
// et casse le volume.
func (volume *Volume) operationError(current *session, operation string, err error) error {
	if err == nil {
		return nil
	}
	connectionLost := isTransportFailure(err) || current.closed.Load()
	select {
	case <-current.done:
		connectionLost = true
	default:
	}
	if !connectionLost {
		return err
	}
	volume.drop(current)
	// %v : pkg/sftp rend « connection lost: EOF », qui porte io.EOF ; une coupure ne doit jamais passer pour une fin
	// de fichier (pièce fausse, fichier raccourci).
	return fmt.Errorf("%w: sftpfs: %s on %s: %v", fsys.ErrRemoteUnavailable, operation, volume.provider.Address, err) //nolint:errorlint // cause citée sans être enveloppée : jamais io.EOF
}

// acquire rend la session vivante, ou en rouvre une si l'étape ne l'a pas déjà fait.
func (volume *Volume) acquire(ctx context.Context) (*session, error) {
	if current := volume.live(); current != nil {
		return current, nil
	}
	volume.redial.Lock()
	defer volume.redial.Unlock()
	volume.mutex.Lock()
	current, mayReconnect := volume.current, !volume.closed && !volume.reconnectUsed
	if current == nil && mayReconnect {
		volume.reconnectUsed = true
	}
	volume.mutex.Unlock()
	if current != nil {
		return current, nil
	}
	if !mayReconnect {
		return nil, fmt.Errorf("%w: sftpfs: %s is broken until the next stage", fsys.ErrRemoteUnavailable, volume.provider.Address)
	}
	return volume.reconnect(ctx)
}

func (volume *Volume) live() *session {
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	return volume.current
}

func (volume *Volume) reconnect(ctx context.Context) (*session, error) {
	current, warnings, err := connect(ctx, volume.provider, volume.deps)
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	if err != nil {
		volume.state = StateBroken
		return nil, fmt.Errorf("%w: sftpfs: reconnect: %w", fsys.ErrRemoteUnavailable, err)
	}
	if volume.closed {
		current.close()
		return nil, fmt.Errorf("%w: sftpfs: %s is closed", fsys.ErrRemoteUnavailable, volume.provider.Address)
	}
	volume.current, volume.state, volume.warnings = current, StateOK, warnings
	volume.watch(current)
	return current, nil
}

// drop ferme une session et, si c'est la courante, passe le volume broken.
func (volume *Volume) drop(current *session) {
	current.close()
	volume.mutex.Lock()
	defer volume.mutex.Unlock()
	if volume.current == current {
		volume.current, volume.state = nil, StateBroken
	}
}

// watch suit la session : sa fin (coupure, fermeture) casse le volume ; le keepalive tourne jusque-là.
func (volume *Volume) watch(current *session) {
	go func() {
		_ = current.sshClient.Wait()
		close(current.done)
		volume.drop(current)
	}()
	go volume.keepalive(current)
}

// keepalive envoie keepalive@openssh.com à chaque intervalle ; trois réponses manquées ferment la session. Un refus
// du serveur est une réponse : la session vit.
func (volume *Volume) keepalive(current *session) {
	interval := volume.provider.Keepalive
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for missed := 0; missed < maxMissedKeepalives; {
		select {
		case <-current.done:
			return
		case <-ticker.C:
		}
		if current.answersKeepalive(interval) {
			missed = 0
		} else {
			missed++
		}
	}
	volume.drop(current)
}

// answersKeepalive attend la réponse au plus wait ; une requête sans réponse reste en attente jusqu'à la fermeture
// de la connexion, qui la libère.
func (current *session) answersKeepalive(wait time.Duration) bool {
	reply := make(chan error, 1)
	go func() {
		_, _, err := current.sshClient.SendRequest(keepaliveRequest, true, nil)
		reply <- err
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case err := <-reply:
		return err == nil
	case <-timer.C:
		return false
	case <-current.done:
		return false
	}
}
