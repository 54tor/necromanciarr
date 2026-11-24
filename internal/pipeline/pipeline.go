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
	"necromanciarr/internal/library"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
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
	SaveCandidate(ctx context.Context, candidate store.Candidate, files []store.CandidateFile, now time.Time) (int64, store.SaveOutcome, error)
	RegroupCandidates(ctx context.Context) error
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
type Deps struct {
	Store        Store
	ScanRoot     RootScanner
	LibraryRoots []string
	ArrSources   []ItemsSource
	Provider     Searcher
	Mapper       PathMapper
	Now          func() time.Time
	LockTTL      time.Duration
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
	StageVerify: notImplemented,
	StageLink:   notImplemented,
	StageSeed:   notImplemented,
}

func notImplemented(context.Context, Deps) (Result, error) {
	return Result{}, ErrStageNotImplemented
}

// Run exécute une étape sous le verrou exclusif du store ; le Result incomplet est renvoyé même en cas d'erreur.
func Run(ctx context.Context, stage Stage, deps Deps) (Result, error) {
	runner, known := stageRunners[stage]
	if !known {
		return Result{}, fmt.Errorf("%w: %q", ErrUnknownStage, stage)
	}
	release, err := acquireLock(ctx, deps, stage)
	if err != nil {
		return Result{Stage: stage}, err
	}
	defer release()
	result, err := runner(ctx, deps)
	result.Stage = stage
	return result, err
}

// acquireLock prend le verrou et le prolonge tant que l'étape tourne ; la fonction renvoyée l'arrête et le rend,
// même après annulation de ctx.
func acquireLock(ctx context.Context, deps Deps, stage Stage) (func(), error) {
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
	stopRefresh := make(chan struct{})
	refreshDone := make(chan struct{})
	go refreshLock(deps, holder, ttl, stopRefresh, refreshDone)
	return func() {
		close(stopRefresh)
		<-refreshDone
		_ = deps.Store.ReleaseRunLock(context.WithoutCancel(ctx), holder)
	}, nil
}

// refreshLock prolonge le bail toutes les ttl/3 ; un échec ponctuel est rattrapé au tour suivant, avant expiration.
func refreshLock(deps Deps, holder string, ttl time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_ = deps.Store.RefreshRunLock(context.Background(), holder, deps.now().Add(ttl))
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
