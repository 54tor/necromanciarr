// Package app est la couche applicative commune à la ligne de commande et au serveur web : chaque action de
// l'opérateur passe par Service, qui s'appuie sur le store et les étapes du pipeline.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/fsys"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// Erreurs typées des actions ; leurs messages ne portent ni URL, ni chemin, ni secret.
var (
	// ErrStaleProposal : la proposition appartient à une génération antérieure de son candidat (fichier changé,
	// réassociation) ; il faut revérifier avant de décider.
	ErrStaleProposal = errors.New("app: stale proposal, re-verify")
	// ErrForceNotAllowed : un pack dont des fichiers média manquent ne peut jamais être démarré par Force en v1.
	ErrForceNotAllowed = errors.New("app: a partial pack can never be forced")
	// ErrConflictNeedsChoose : un litige ne s'accepte pas, l'opérateur choisit le torrent avec Choose.
	ErrConflictNeedsChoose = errors.New("app: a conflict cannot be accepted, choose a torrent instead")
	// ErrPartialPackNeedsForce : un pack dont des fichiers média manquent est ignoré par défaut.
	ErrPartialPackNeedsForce = errors.New("app: a partial pack needs the explicit allow-partial-pack option")
	// ErrNotATorrentProposal : la proposition ne lie aucun torrent (dedupe, repair : actions dédiées).
	ErrNotATorrentProposal = errors.New("app: this proposal does not link a torrent")
	// ErrCandidateAlreadyAccepted : une autre proposition du même torrent est déjà acceptée ou appliquée.
	ErrCandidateAlreadyAccepted = errors.New("app: another proposal of this torrent is already accepted")
	// ErrOneTorrentPerFile : en mode cross-seed désactivé, un fichier ne sert qu'à un seul torrent.
	ErrOneTorrentPerFile = errors.New("app: cross-seed is disabled, another torrent of this group is already accepted")
	// ErrNotFull : seule une proposition full peut devenir la principale de son groupe.
	ErrNotFull = errors.New("app: only a full proposal can be chosen")
	// ErrCrossSeedNotNative : une proposition cross_seed ne s'accepte qu'en mode cross-seed natif effectif.
	ErrCrossSeedNotNative = errors.New("app: a cross_seed proposal can only be accepted in native cross-seed mode")
	// ErrNotForceable : Force ne démarre qu'un partial_annex déjà lié et laissé en pause par Seed.
	ErrNotForceable = errors.New("app: only a linked partial_annex left paused by seed can be forced")
	// ErrForceIsolationImpossible : un média à cheval sur une pièce manquante ne peut être remplacé par une copie
	// indépendante (ni reflink ni copie, ou place insuffisante) ; rien n'a changé et le torrent reste en pause.
	ErrForceIsolationImpossible = errors.New("app: force refused, a straddling media file cannot be copied out of the library")
	// ErrForceServerCopyUnavailable : le dossier de liens est sur un volume distant sans copie côté serveur
	// (copy-data) ; rien n'est copié (jamais de copie par flux), rien n'a changé.
	ErrForceServerCopyUnavailable = errors.New("app: force refused, the remote volume offers no server-side copy")
	// ErrForceFreeSpaceUnknown : la place libre du dossier de liens distant ne peut pas être lue ; rien n'a changé.
	ErrForceFreeSpaceUnknown = errors.New("app: force refused, the free space of the remote link folder is unknown")
	// ErrNoPrimary : le groupe n'a pas de proposition principale full ouverte (Choose d'abord).
	ErrNoPrimary = errors.New("app: the group has no open primary full proposal")
	// ErrGroupNotFound : aucun candidat vérifié n'appartient à ce groupe.
	ErrGroupNotFound = errors.New("app: group not found")
	// ErrNotConfigured : l'étape demandée a besoin d'un composant absent de la configuration (nommé dans le message).
	ErrNotConfigured = errors.New("app: component not configured")
)

// Service regroupe les actions de l'opérateur ; c'est la seule porte d'entrée de la CLI et du web.
type Service interface {
	// RunStage exécute une étape du pipeline sous son verrou.
	RunStage(ctx context.Context, stage pipeline.Stage) (pipeline.Result, error)
	// Status résume l'état des torrents, des propositions et du mode cross-seed.
	Status(ctx context.Context) (Status, error)
	// ListProposals liste les propositions qui passent le filtre, avec leurs détails.
	ListProposals(ctx context.Context, filter ProposalFilter) ([]Proposal, error)
	// Accept accepte une proposition de torrent de la génération courante.
	Accept(ctx context.Context, proposalID int64, options ...DecisionOption) error
	// Reject rejette une proposition.
	Reject(ctx context.Context, proposalID int64) error
	// Choose fait d'une proposition full la principale de son groupe (choix de l'opérateur).
	Choose(ctx context.Context, proposalID int64) error
	// Force démarre un partial_annex lié et laissé en pause, après décision explicite de l'opérateur.
	Force(ctx context.Context, proposalID int64) (ForceResult, error)
	// AcceptGroup accepte la principale du groupe, plus les autres torrents vérifiés en mode Native.
	AcceptGroup(ctx context.Context, groupID int64) ([]int64, error)
	// Doctor diagnostique l'installation (contrôles choisis, délai par contrôle).
	Doctor(ctx context.Context, options DoctorOptions) (doctor.Report, error)
	// TestCandidate teste une valeur saisie (radarr, sonarr, qbittorrent) avant son enregistrement ; une valeur invalide
	// est une erreur d'usage, sans requête sortante.
	TestCandidate(ctx context.Context, candidate SetupCandidate) (SetupTestResult, error)
	// ApplyFix applique un correctif en un clic proposé par le diagnostic ; une cible hors de la configuration est une
	// erreur d'usage, sans rien créer.
	ApplyFix(ctx context.Context, fixID string, target string) error
	// DedupePlan reprend les remplacements interrompus puis propose de remplacer, côté torrent, les copies de fichiers
	// de la bibliothèque par des liens (propositions en attente, jamais appliquées d'office).
	DedupePlan(ctx context.Context) ([]DedupeProposal, error)
	// DedupeAccept applique une proposition dedupe acceptée (AcceptPending : l'accepte d'abord si elle attend ; DryRun :
	// vérifie sans rien créer ni décider).
	DedupeAccept(ctx context.Context, proposalID int64, options ...DecisionOption) (MaintenanceResult, error)
	// RepairScan cherche les liens cassés (enregistrés, ou inconnus sous directory) et propose leur nouvelle cible.
	RepairScan(ctx context.Context, directory string) ([]RepairProposal, error)
	// RepairAccept applique une proposition repair acceptée (mêmes options que DedupeAccept).
	RepairAccept(ctx context.Context, proposalID int64, options ...DecisionOption) (MaintenanceResult, error)
	// ListLinks liste les liens enregistrés avec leur état courant, lu sur le disque sans rien écrire, par page bornée.
	ListLinks(ctx context.Context, filter LinkFilter) (LinkPage, error)
	// ListSources décrit la chaîne de sources dans son ordre : sorte, tracker, état, usage du jour et dernière erreur
	// (code stable), sans requête réseau.
	ListSources(ctx context.Context) ([]SourceRow, error)
	// TestSource envoie t=caps et une recherche vide à un indexeur, au quota du jour ; l'échec est un code stable.
	TestSource(ctx context.Context, name string) (SourceTestResult, error)
	// ListVolumes décrit le disque local et chaque volume sftp : adresse hôte:port seule, état de la session,
	// dernière sonde enregistrée et sa date, sans connexion.
	ListVolumes(ctx context.Context) ([]VolumeRow, error)
	// ProbeVolume sonde à nouveau un volume sftp dans son premier dossier de liens, sous le verrou des étapes.
	ProbeVolume(ctx context.Context, name string) (VolumeProbeResult, error)
	// VolumeCommands rend les lignes ssh exec exactes qu'une garde distante doit admettre pour ce volume.
	VolumeCommands(ctx context.Context, name string) (VolumeCommands, error)
	// ConfigView rend la configuration éditable (réglages, listes résolues, surcharges orphelines), sans secret.
	ConfigView(ctx context.Context) (ConfigView, error)
	// SetSetting enregistre un réglage (scellé s'il est secret) après validation de la configuration candidate.
	SetSetting(ctx context.Context, key string, value json.RawMessage) error
	// UnsetSetting retire un réglage de la base.
	UnsetSetting(ctx context.Context, key string) error
	// PutItem enregistre un élément de liste ; un secret absent ou null garde la valeur enregistrée.
	PutItem(ctx context.Context, list, itemID string, element map[string]any) error
	// DeleteItem masque un élément cross-seed ou supprime un élément ajouté.
	DeleteItem(ctx context.Context, list, itemID string) error
	// UnsetItem retire la surcharge d'un élément (retour à cross-seed).
	UnsetItem(ctx context.Context, list, itemID string) error
	// Actions renvoie le registre des actions (parité CLI et web), descriptions dans la langue du Service.
	Actions() []Action
	// Translator donne le traducteur de la langue configurée, que la CLI et le web utilisent pour tout texte rendu.
	Translator() i18n.Translator
}

// DecisionOption est une option explicite d'Accept, de DedupeAccept ou de RepairAccept.
type DecisionOption int

const (
	// AllowPartialPack autorise un pack dont des fichiers média manquent (ignoré par défaut).
	AllowPartialPack DecisionOption = iota + 1
	// DryRun fait toutes les vérifications d'une application dedupe ou repair sans rien créer ni décider.
	DryRun
	// AcceptPending accepte d'abord une proposition dedupe ou repair en attente, après le contrôle de sa nature.
	AcceptPending
)

// ForceResult rend compte d'un Force : Warnings rappelle toujours les avertissements de la proposition ; Replaced
// nomme les médias du dossier de liens remplacés par une copie indépendante avant la reprise.
type ForceResult struct {
	ProposalID int64                   `json:"proposal_id"`
	Forced     bool                    `json:"forced"`
	Warnings   []Warning               `json:"warnings"`
	Replaced   []pipeline.IsolatedFile `json:"replaced,omitempty"`
}

// Store est la part du store utilisée par les actions.
type Store interface {
	ListProposals(ctx context.Context, decision store.ProposalDecision) ([]store.Proposal, error)
	ListCandidatesByState(ctx context.Context, states ...store.CandidateState) ([]store.Candidate, error)
	ListProposalProgress(ctx context.Context) (map[int64]store.ProposalProgress, error)
	ListCandidateHolds(ctx context.Context) ([]store.CandidateHold, error)
	SaveProposalProgress(ctx context.Context, progress store.ProposalProgress, now time.Time) error
	SetProposalDecision(ctx context.Context, proposalID int64, decision store.ProposalDecision, now time.Time) error
	AcceptProposals(ctx context.Context, proposalIDs []int64, now time.Time) error
	LoadVerifiedAssignment(ctx context.Context, candidateID int64) (store.VerifiedAssignment, bool, error)
	SetCandidateState(ctx context.Context, candidateID int64, state store.CandidateState, now time.Time) error
	SetGroupPrimary(ctx context.Context, groupID, proposalID int64, source store.PrimarySource) error
	CreateProposal(ctx context.Context, proposal store.Proposal, now time.Time) (int64, error)
	AddProposalWarning(ctx context.Context, proposalID int64, warning i18n.Message) error
	ListMediaFiles(ctx context.Context) ([]store.MediaFile, error)
	ListLinks(ctx context.Context, state store.LinkState) ([]store.Link, error)
	RecordLink(ctx context.Context, link store.Link) error
	UpdateLinkTarget(ctx context.Context, linkPath, targetPath string) error
	SetLinkState(ctx context.Context, linkPath string, state store.LinkState, verifiedAt time.Time) error
	LookupFingerprint(ctx context.Context, path string, size int64, modTime time.Time) (string, bool, error)
	RecordFingerprint(ctx context.Context, entry store.CachedFingerprint) error
	LoadMountCapabilities(ctx context.Context, mountPath string) (store.MountCapabilities, bool, error)
	SaveMountCapabilities(ctx context.Context, capabilities store.MountCapabilities) error
	CountIndexerUsage(ctx context.Context, indexer, kind string, since time.Time) (int, error)
	VolumeCapabilities(ctx context.Context, volume string) (fsys.Capabilities, time.Time, bool, error)
	SaveVolumeCapabilities(ctx context.Context, volume string, capabilities fsys.Capabilities, at time.Time) error
	SealedClearer
}

// Resumer démarre un torrent du client ; *qbit.Client convient.
type Resumer interface {
	Resume(ctx context.Context, infoHash string) error
}

// Deps regroupe les dépendances du Service : Stages sert aux étapes (même store, mêmes clients), Client à Force (nil =
// aucun client), CrossSeed et CrossSeedWarnings viennent de crossseed.ResolveMode, Now nil vaut time.Now. HashSources
// est le registre des fournisseurs d'empreintes, construit une fois pour les actions dedupe et repair. Config donne
// au diagnostic et à dedupe et repair leurs réglages (racines, section dedupe, dossiers de liens) ; DoctorDeps remplace
// les accès extérieurs du diagnostic (en production, seule la sonde des blocs scellés y est posée) ; Maintenance est le client torrent de dedupe et repair
// (nil = ni recontrôle ni recherche par torrent). dedupe et repair prennent le verrou des étapes de Stages.Store.
type Deps struct {
	Store             Store
	Stages            pipeline.Deps
	Client            Resumer
	CrossSeed         crossseed.Behavior
	CrossSeedWarnings []i18n.Message
	HashSources       hashsource.Registry
	// RemoteHashes calcule les empreintes sur l'hôte des volumes sftp dont la sonde annonce RemoteHash (sftp-exec) ;
	// nil : aucune, un fichier distant est relu.
	RemoteHashes hashsource.HashSource
	Config       config.Config
	DoctorDeps   doctor.Deps
	Maintenance  MaintenanceClient
	Now          func() time.Time
	// Translator rend les textes dans la langue configurée (vide : français, sans masquage).
	Translator i18n.Translator
	// ResolveCrossSeed, s'il est posé, redonne le mode cross-seed au début de chaque étape, acceptation et Status
	// (production : sonde TCP mise en cache) ; nil garde CrossSeed, CrossSeedWarnings et Stages.Notifier fixes.
	ResolveCrossSeed func(ctx context.Context) CrossSeedResolution
	// Volumes donne les volumes sftp du registre par nom de fournisseur (état, sonde) ; nil : aucun.
	Volumes map[string]VolumeHandle
	// Logger reçoit les lignes codées de la couche applicative (nil : slog.Default).
	Logger *slog.Logger
	// ConfigStore et LoadStoredConfig servent à l'édition de la configuration ; nil : édition indisponible.
	ConfigStore      ConfigStore
	LoadStoredConfig StoredConfigLoader
}

// inspect et inspectionBudget remplacent, dans les tests seulement, l'examen d'un lien et le budget de ListLinks ;
// inspecting est vrai tant qu'un examen des liens tourne (un seul à la fois).
type service struct {
	deps             Deps
	inspect          func(store.Link) repair.Inspection
	inspectionBudget time.Duration
	inspecting       atomic.Bool
	// sourceTests garde les indexeurs dont un sources test est en cours : un second test simultané est refusé.
	sourceTests sync.Map
	// volumeProbes garde les volumes dont un volumes probe est en cours : une seconde sonde simultanée est refusée.
	volumeProbes sync.Map
	// configEdits sérialise les écritures de configuration ; restartRequired dit qu'une écriture attend une relance.
	configEdits     sync.Mutex
	restartRequired atomic.Bool
}

// New construit le Service sur ses dépendances.
func New(deps Deps) Service {
	if deps.Translator.Language() == "" {
		deps.Translator = i18n.New(i18n.French, nil)
	}
	if deps.DoctorDeps.CapsCache == nil {
		deps.DoctorDeps.CapsCache = doctor.NewCapsCache(doctor.DefaultCapsCacheTTL, deps.Now)
	}
	if deps.DoctorDeps.Registry == nil {
		deps.DoctorDeps.Registry = deps.Stages.Registry
	}
	if deps.DoctorDeps.SealedProbe == nil {
		deps.DoctorDeps.SealedProbe = pathProbe{databasePath: config.ResolveDBPath(deps.Config)}
	}
	return &service{deps: deps}
}

func (svc *service) Translator() i18n.Translator { return svc.deps.Translator }

func (svc *service) now() time.Time {
	if svc.deps.Now == nil {
		return time.Now()
	}
	return svc.deps.Now()
}

func (svc *service) RunStage(ctx context.Context, stage pipeline.Stage) (pipeline.Result, error) {
	if err := svc.checkStageDeps(stage); err != nil {
		return pipeline.Result{Stage: stage}, err
	}
	stages := svc.deps.Stages
	stages.Announces = svc.announces()
	resolution := svc.crossSeed(ctx)
	stages.CrossSeed, stages.CrossSeedWarnings, stages.Notifier = resolution.Behavior, resolution.Warnings, resolution.Notifier
	result, err := pipeline.Run(ctx, stage, stages)
	return withRenderedTexts(svc.deps.Translator, result), err
}

// checkStageDeps refuse proprement une étape dont un composant manque, au lieu d'échouer au milieu.
func (svc *service) checkStageDeps(stage pipeline.Stage) error {
	stages := svc.deps.Stages
	switch {
	case (stage == pipeline.StageMatch || stage == pipeline.StageVerify) && stages.Sources == nil:
		return fmt.Errorf("%w: library provider", ErrNotConfigured)
	case stage == pipeline.StageSeed && stages.Client == nil:
		return fmt.Errorf("%w: torrent client", ErrNotConfigured)
	}
	return nil
}

// mapStale traduit le refus de génération du store en message pour l'opérateur.
func mapStale(err error) error {
	if errors.Is(err, store.ErrStaleGeneration) {
		return fmt.Errorf("%w: %w", ErrStaleProposal, err)
	}
	return err
}
