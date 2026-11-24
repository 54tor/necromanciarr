// Package pipeline enchaîne les étapes (inventaire, recherche, vérification, liens, seed) sur l'état persisté du store ;
// chaque étape est rejouable et une seule s'exécute à la fois.
package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"necromanciarr/internal/arr"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/library"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// Stage est une étape du pipeline.
type Stage string

// Étapes du pipeline, dans l'ordre d'exécution.
const (
	StageScan   Stage = "scan"
	StageMatch  Stage = "match"
	StageVerify Stage = "verify"
	StageLink   Stage = "link"
	StageSeed   Stage = "seed"
)

// Erreurs typées du pipeline.
var (
	ErrUnknownStage        = errors.New("pipeline: unknown stage")
	ErrStageNotImplemented = errors.New("pipeline: stage not implemented yet")
	ErrStageRunning        = errors.New("pipeline: another stage is running")
	// ErrLockLost signale que le verrou d'étape a été repris ou n'a pas pu être prolongé : l'étape s'est arrêtée.
	ErrLockLost = errors.New("pipeline: run lock lost")
)

// defaultLockTTL est le bail du verrou d'étape : un processus mort libère la place au plus tard après ce délai.
const defaultLockTTL = 2 * time.Minute

// ParseStage lit un nom d'étape, sans tenir compte de la casse.
func ParseStage(raw string) (Stage, error) {
	stage := Stage(strings.ToLower(strings.TrimSpace(raw)))
	if _, known := stageRunners[stage]; !known {
		return "", fmt.Errorf("%w: %q", ErrUnknownStage, raw)
	}
	return stage, nil
}

// Store est la part du store utilisée par les étapes.
type Store interface {
	ListMediaFiles(ctx context.Context) ([]store.MediaFile, error)
	SetMediaFileIDs(ctx context.Context, mediaFileID int64, tmdbID, tvdbID int) error
	DeleteMediaFile(ctx context.Context, mediaFileID int64, now time.Time) error
	InvalidateCandidatesForFile(ctx context.Context, mediaFileID int64, now time.Time) (int, error)
	MarkChangedCandidatesStale(ctx context.Context, now time.Time) (int, error)
	ListCandidatesByState(ctx context.Context, states ...store.CandidateState) ([]store.Candidate, error)
	ListCandidateFiles(ctx context.Context, candidateID int64) ([]store.CandidateFile, error)
	SaveCandidate(ctx context.Context, candidate store.Candidate, files []store.CandidateFile, now time.Time) (int64, store.SaveOutcome, error)
	RegroupCandidates(ctx context.Context) error
	SetCandidateState(ctx context.Context, candidateID int64, state store.CandidateState, now time.Time) error
	SaveCandidateTorrent(ctx context.Context, candidateID int64, raw []byte, now time.Time) error
	CandidateTorrent(ctx context.Context, candidateID int64) ([]byte, error)
	LoadVerification(ctx context.Context, candidateID int64) (store.Verification, bool, error)
	SaveVerification(ctx context.Context, verification store.Verification, now time.Time) error
	ListVerifiedFiles(ctx context.Context, candidateID int64) ([]store.VerifiedFile, error)
	SaveVerifiedFile(ctx context.Context, file store.VerifiedFile) error
	UpsertProposal(ctx context.Context, proposal store.Proposal, now time.Time) (int64, bool, error)
	ListProposals(ctx context.Context, decision store.ProposalDecision) ([]store.Proposal, error)
	SetGroupPrimary(ctx context.Context, groupID, proposalID int64) error
	AcquireRunLock(ctx context.Context, holder, stage string, now, expiresAt time.Time) error
	RefreshRunLock(ctx context.Context, holder string, expiresAt time.Time) error
	ReleaseRunLock(ctx context.Context, holder string) error
}

// ItemsSource liste les fichiers connus d'une instance Radarr ou Sonarr.
type ItemsSource interface {
	Items(ctx context.Context) ([]arr.Item, error)
}

// Searcher cherche des releases chez le fournisseur de bibliothèque.
type Searcher interface {
	Search(ctx context.Context, query libraryprovider.Query) ([]libraryprovider.Release, error)
}

// TorrentFetcher télécharge le .torrent et les .nfo d'une release chez le fournisseur de bibliothèque.
type TorrentFetcher interface {
	Torrent(ctx context.Context, infohash, tracker string) ([]byte, error)
	Nfos(ctx context.Context, infohash, tracker string) ([]libraryprovider.Nfo, error)
}

// PathMapper traduit un chemin vu par un service externe (côté To des règles) vers la vue de ce processus (côté From).
type PathMapper interface {
	Reverse(original string) (string, error)
}

// RootScanner inventorie une racine de bibliothèque dans le store.
type RootScanner func(ctx context.Context, root string) error

// LibraryScanner adapte library.Scan au pipeline.
func LibraryScanner(mediaStore *store.Store) RootScanner {
	return func(ctx context.Context, root string) error { return library.Scan(ctx, root, mediaStore) }
}

// Deps regroupe les dépendances des étapes. LibraryRoots sont vus par ce processus ; Mapper nil signifie que les
// services externes voient les mêmes chemins ; Now nil vaut time.Now ; LockTTL 0 vaut deux minutes.
//
// Pour Verify : ReadLimiter plafonne le débit de lecture locale (nil = sans plafond), ReadBudget les octets lus pour
// départager des fichiers de même taille (0 = sans limite) ; OpenLocal nil ouvre en lecture seule avec os.Open ;
// CrossSeed vient de crossseed.ResolveMode ; l'avancement est enregistré toutes les ProgressEvery pièces (0 = 64) ou
// toutes les ProgressInterval (0 = 5 s).
type Deps struct {
	Store            Store
	ScanRoot         RootScanner
	LibraryRoots     []string
	ArrSources       []ItemsSource
	Provider         Searcher
	Fetcher          TorrentFetcher
	Mapper           PathMapper
	ReadLimiter      torrentcheck.Limiter
	ReadBudget       int64
	OpenLocal        func(path string) (LocalFile, error)
	CrossSeed        crossseed.Behavior
	ProgressEvery    int
	ProgressInterval time.Duration
	Now              func() time.Time
	LockTTL          time.Duration
}

func (deps Deps) now() time.Time {
	if deps.Now == nil {
		return time.Now()
	}
	return deps.Now()
}

func (deps Deps) lockTTL() time.Duration {
	if deps.LockTTL <= 0 {
		return defaultLockTTL
	}
	return deps.LockTTL
}

// Result résume une exécution : Reasons compte les éléments ignorés ou en échec par nom de raison (jamais de chemin
// complet ni de secret) ; UnidentifiedSamples donne les premiers noms de fichiers sans identifiant externe.
type Result struct {
	Stage               Stage
	Processed           int
	Skipped             int
	Failed              int
	Reasons             map[string]int
	UnidentifiedFiles   int
	UnidentifiedSamples []string
	CandidatesCreated   int
	CandidatesUpdated   int
	CandidatesUnchanged int
	CandidatesStale     int
	// Compteurs de Verify : candidats vérifiés, classés partial_annex ou partial_pack, propositions de conflit créées,
	// vérifications reprises, et propositions créées au total.
	Verified         int
	PartialAnnex     int
	PartialPack      int
	Conflicts        int
	Resumed          int
	ProposalsCreated int
}

func (result *Result) count(reason string, amount int) {
	if amount == 0 {
		return
	}
	if result.Reasons == nil {
		result.Reasons = map[string]int{}
	}
	result.Reasons[reason] += amount
}

// ResumableError arrête une étape proprement (fournisseur limité ou indisponible) : ce qui est déjà fait est
// enregistré et l'étape peut être relancée après RetryAfter (zéro si inconnu).
type ResumableError struct {
	Stage      Stage
	RetryAfter time.Duration
	Cause      error
}

// Error décrit l'arrêt sans exposer d'URL.
func (resumable *ResumableError) Error() string {
	return fmt.Sprintf("pipeline %s stopped, retry after %s: %v", resumable.Stage, resumable.RetryAfter, resumable.Cause)
}

// Unwrap expose la cause.
func (resumable *ResumableError) Unwrap() error { return resumable.Cause }

type stageRunner func(ctx context.Context, deps Deps) (Result, error)

var stageRunners = map[Stage]stageRunner{
	StageScan:   runScan,
	StageMatch:  runMatch,
	StageVerify: runVerify,
	StageLink:   notImplemented,
	StageSeed:   notImplemented,
}

func notImplemented(context.Context, Deps) (Result, error) {
	return Result{}, ErrStageNotImplemented
}

// Run exécute une étape sous le verrou exclusif du store ; le Result incomplet est renvoyé même en cas d'erreur.
// Si le verrou est perdu en cours de route, le contexte de l'étape est annulé et Run renvoie ErrLockLost.
func Run(ctx context.Context, stage Stage, deps Deps) (Result, error) {
	runner, known := stageRunners[stage]
	if !known {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownStage, stage)
	}
	runContext, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	release, err := acquireLock(runContext, deps, stage, cancelRun)
	if err != nil {
		return Result{Stage: stage}, err
	}
	defer release()
	result, err := runner(runContext, deps)
	result.Stage = stage
	if lockLost(runContext) {
		return result, fmt.Errorf("pipeline %s: %w", stage, ErrLockLost)
	}
	return result, err
}

func lockLost(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrLockLost)
}

// acquireLock prend le verrou et le prolonge tant que l'étape tourne ; la fonction renvoyée l'arrête et le rend,
// même après annulation de ctx. onLost annule l'étape si le verrou ne peut plus être prolongé.
func acquireLock(ctx context.Context, deps Deps, stage Stage, onLost context.CancelCauseFunc) (func(), error) {
	holder, err := newHolderID()
	if err != nil {
		return nil, err
	}
	ttl := deps.lockTTL()
	now := deps.now()
	if err := deps.Store.AcquireRunLock(ctx, holder, string(stage), now, now.Add(ttl)); err != nil {
		if errors.Is(err, store.ErrRunLocked) {
			return nil, fmt.Errorf("%w: %w", ErrStageRunning, err)
		}
		return nil, err
	}
	refresher := lockRefresher{deps: deps, holder: holder, ttl: ttl, onLost: onLost}
	stopRefresh := make(chan struct{})
	refreshDone := make(chan struct{})
	go refresher.run(context.WithoutCancel(ctx), stopRefresh, refreshDone)
	return func() {
		close(stopRefresh)
		<-refreshDone
		_ = deps.Store.ReleaseRunLock(context.WithoutCancel(ctx), holder)
	}, nil
}

// maxRefreshFailures est le nombre d'échecs consécutifs de prolongation tolérés : à TTL/3 d'intervalle, le troisième
// échec arrive juste avant l'expiration du bail.
const maxRefreshFailures = 3

// minRefreshInterval évite un intervalle nul (time.NewTicker panique) pour un bail très court.
const minRefreshInterval = time.Millisecond

type lockRefresher struct {
	deps   Deps
	holder string
	ttl    time.Duration
	onLost context.CancelCauseFunc
}

// run prolonge le bail toutes les TTL/3 ; verrou repris par un autre ou trois échecs de suite : l'étape est annulée.
func (refresher lockRefresher) run(ctx context.Context, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(max(refresher.ttl/3, minRefreshInterval))
	defer ticker.Stop()
	consecutiveFailures := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			err := refresher.deps.Store.RefreshRunLock(ctx, refresher.holder, refresher.deps.now().Add(refresher.ttl))
			if err == nil {
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			if errors.Is(err, store.ErrRunLocked) || consecutiveFailures >= maxRefreshFailures {
				refresher.onLost(fmt.Errorf("%w: %w", ErrLockLost, err))
				return
			}
		}
	}
}

func newHolderID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("lock holder id: %w", err)
	}
	return hex.EncodeToString(random), nil
}
