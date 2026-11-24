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
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
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
	ErrUnknownStage = errors.New("pipeline: unknown stage")
	ErrStageRunning = errors.New("pipeline: another stage is running")
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
	SetGroupPrimary(ctx context.Context, groupID, proposalID int64, source store.PrimarySource) error
	ClearGroupPrimary(ctx context.Context, groupID int64) error
	SetProposalGroup(ctx context.Context, proposalID, groupID int64) error
	SetProposalDecision(ctx context.Context, proposalID int64, decision store.ProposalDecision, now time.Time) error
	CompleteVerification(ctx context.Context, verification store.Verification, files []store.AssignedFile, now time.Time) error
	LoadVerifiedAssignment(ctx context.Context, candidateID int64) (store.VerifiedAssignment, bool, error)
	ListLinks(ctx context.Context, state store.LinkState) ([]store.Link, error)
	RecordLink(ctx context.Context, link store.Link) error
	LookupFingerprint(ctx context.Context, path string, size int64, modTime time.Time) (string, bool, error)
	RecordFingerprint(ctx context.Context, entry store.CachedFingerprint) error
	SaveCandidateHold(ctx context.Context, hold store.CandidateHold, now time.Time) error
	ListCandidateHolds(ctx context.Context) ([]store.CandidateHold, error)
	DeleteCandidateHold(ctx context.Context, candidateID int64) error
	ListProposalProgress(ctx context.Context) (map[int64]store.ProposalProgress, error)
	SaveProposalProgress(ctx context.Context, progress store.ProposalProgress, now time.Time) error
	ApplyProposal(ctx context.Context, proposalID int64, step store.ProposalStep, linkRoot string, now time.Time) error
	AddProposalWarning(ctx context.Context, proposalID int64, warning string) error
	ClearProposalStep(ctx context.Context, proposalID int64, now time.Time) error
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

// ClientPathMapper traduit un chemin de ce processus (côté From des règles) vers la vue du client torrent (côté To).
type ClientPathMapper interface {
	Translate(original string) (string, error)
}

// TorrentClient est la part du client torrent utilisée par Seed et par Force ; *qbit.Client convient.
type TorrentClient interface {
	Add(ctx context.Context, torrent []byte, savePath string, options qbit.AddOptions) error
	FindTorrent(ctx context.Context, infoHash string) (qbit.TorrentInfo, bool, error)
	Pause(ctx context.Context, infoHash string) error
	RemoveKeepingData(ctx context.Context, infoHash string) error
	RecheckAndGuard(ctx context.Context, infoHash string, timeout time.Duration) (qbit.RecheckResult, error)
	Resume(ctx context.Context, infoHash string) error
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
//
// Pour Link : LinkDirs (vue de ce processus) et LinkKinds, ordre des sortes de liens autorisées (vide = hardlink) ;
// ProbeLinks nil vaut linker.Probe ; PrepareCopy nil vaut linker.PrepareCopy (copie indépendante posée avant un
// Force). Pour Seed : Client, ClientMapper (nil = même vue que le client), Category et Tags,
// SkipRecheck (permis de sauter le recontrôle quand notre vérification couvre 100 %), RecheckTimeout (0 = une heure),
// Notifier (nil = aucun) et CrossSeedWarning, l'avertissement de crossseed.ResolveMode recopié dans le Result.
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
	// PartialMaxMissingPercent plafonne la part de pièces manquantes d'un partial_annex ou partial_pack (nil = sans
	// plafond) : au-delà, le verdict est mis de côté au lieu de devenir une proposition.
	PartialMaxMissingPercent *float64
	LinkDirs                 []string
	LinkKinds                []linker.Kind
	ProbeLinks               func(directory string) (linker.Capabilities, error)
	PrepareCopy              func(source, destination, root string) (linker.PreparedCopy, error)
	Client                   TorrentClient
	ClientMapper             ClientPathMapper
	Category                 string
	Tags                     []string
	SkipRecheck              bool
	RecheckTimeout           time.Duration
	Notifier                 crossseed.Notifier
	CrossSeedWarning         string
	Now                      func() time.Time
	LockTTL                  time.Duration
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
	Stage               Stage          `json:"stage"`
	Processed           int            `json:"processed"`
	Skipped             int            `json:"skipped"`
	Failed              int            `json:"failed"`
	Reasons             map[string]int `json:"reasons,omitempty"`
	UnidentifiedFiles   int            `json:"unidentified_files"`
	UnidentifiedSamples []string       `json:"unidentified_samples,omitempty"`
	CandidatesCreated   int            `json:"candidates_created"`
	CandidatesUpdated   int            `json:"candidates_updated"`
	CandidatesUnchanged int            `json:"candidates_unchanged"`
	CandidatesStale     int            `json:"candidates_stale"`
	// Totaux de Verify : candidat vérifié, classé partial_annex ou partial_pack, proposition de litige créée,
	// vérification reprise, et proposition créée (toute nature).
	Verified         int `json:"verified"`
	PartialAnnex     int `json:"partial_annex"`
	PartialPack      int `json:"partial_pack"`
	Conflicts        int `json:"conflicts"`
	Resumed          int `json:"resumed"`
	ProposalsCreated int `json:"proposals_created"`
	// Totaux de Link et Seed : torrent lié, ajouté puis repris à 100 %, ajouté et laissé en pause car incomplet.
	Linked                 int `json:"linked"`
	Seeded                 int `json:"seeded"`
	SeededPausedIncomplete int `json:"seeded_paused_incomplete"`
	// Warnings porte les avertissements globaux de l'étape (mode cross-seed), sans URL ni secret.
	Warnings []string `json:"warnings,omitempty"`
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
	StageLink:   runLink,
	StageSeed:   runSeed,
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

// WithLock exécute work sous le verrou unique des étapes, nommé name (dedupe, repair) : une action de maintenance ne
// tourne jamais en même temps qu'une étape ni qu'une autre action. Verrou perdu : ErrLockLost.
func WithLock(ctx context.Context, deps Deps, name string, work func(ctx context.Context) error) error {
	runContext, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	release, err := acquireLock(runContext, deps, Stage(name), cancelRun)
	if err != nil {
		return err
	}
	defer release()
	err = work(runContext)
	if lockLost(runContext) {
		return fmt.Errorf("pipeline %s: %w", name, ErrLockLost)
	}
	return err
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
