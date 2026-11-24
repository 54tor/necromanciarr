// Package app est la couche applicative commune à la ligne de commande et au serveur web : chaque action de
// l'opérateur passe par Service, qui s'appuie sur le store et les étapes du pipeline.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// StaleProposalMessage est le texte montré à l'opérateur pour ErrStaleProposal (couche CLI et web seulement).
const StaleProposalMessage = "proposition périmée, revérifier"

// Erreurs typées des actions ; leurs messages ne portent ni URL, ni chemin, ni secret.
var (
	// ErrStaleProposal : la proposition appartient à une génération antérieure de son candidat (fichier changé,
	// réassociation) ; il faut revérifier avant de décider. La CLI l'affiche avec StaleProposalMessage.
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
	// ErrNotForceable : Force ne démarre qu'un partial_annex déjà lié et laissé en pause par Seed.
	ErrNotForceable = errors.New("app: only a linked partial_annex left paused by seed can be forced")
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
	// Actions renvoie le registre des actions (parité CLI et web).
	Actions() []Action
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

// ForceResult rend compte d'un Force : Warnings rappelle toujours les avertissements de la proposition.
type ForceResult struct {
	ProposalID int64    `json:"proposal_id"`
	Forced     bool     `json:"forced"`
	Warnings   []string `json:"warnings"`
}

// Store est la part du store utilisée par les actions.
type Store interface {
	ListProposals(ctx context.Context, decision store.ProposalDecision) ([]store.Proposal, error)
	ListCandidatesByState(ctx context.Context, states ...store.CandidateState) ([]store.Candidate, error)
	ListProposalProgress(ctx context.Context) (map[int64]store.ProposalProgress, error)
	SaveProposalProgress(ctx context.Context, progress store.ProposalProgress, now time.Time) error
	SetProposalDecision(ctx context.Context, proposalID int64, decision store.ProposalDecision, now time.Time) error
	AcceptProposals(ctx context.Context, proposalIDs []int64, now time.Time) error
	LoadVerifiedAssignment(ctx context.Context, candidateID int64) (store.VerifiedAssignment, bool, error)
	SetCandidateState(ctx context.Context, candidateID int64, state store.CandidateState, now time.Time) error
	SetGroupPrimary(ctx context.Context, groupID, proposalID int64, source store.PrimarySource) error
	CreateProposal(ctx context.Context, proposal store.Proposal, now time.Time) (int64, error)
	AddProposalWarning(ctx context.Context, proposalID int64, warning string) error
	ListMediaFiles(ctx context.Context) ([]store.MediaFile, error)
	ListLinks(ctx context.Context, state store.LinkState) ([]store.Link, error)
	RecordLink(ctx context.Context, link store.Link) error
	UpdateLinkTarget(ctx context.Context, linkPath, targetPath string) error
	SetLinkState(ctx context.Context, linkPath string, state store.LinkState, verifiedAt time.Time) error
	LookupFingerprint(ctx context.Context, path string, size int64, modTime time.Time) (string, bool, error)
	RecordFingerprint(ctx context.Context, entry store.CachedFingerprint) error
}

// Resumer démarre un torrent du client ; *qbit.Client convient.
type Resumer interface {
	Resume(ctx context.Context, infoHash string) error
}

// Deps regroupe les dépendances du Service : Stages sert aux étapes (même store, mêmes clients), Client à Force (nil =
// aucun client), CrossSeed et CrossSeedWarning viennent de crossseed.ResolveMode, Now nil vaut time.Now. HashSources
// est le registre des fournisseurs d'empreintes, construit une fois pour les actions dedupe et repair. Config donne
// au diagnostic et à dedupe et repair leurs réglages (racines, section dedupe, dossiers de liens) ; DoctorDeps remplace
// les accès extérieurs du diagnostic (vide en production) ; Maintenance est le client torrent de dedupe et repair
// (nil = ni recontrôle ni recherche par torrent). dedupe et repair prennent le verrou des étapes de Stages.Store.
type Deps struct {
	Store            Store
	Stages           pipeline.Deps
	Client           Resumer
	CrossSeed        crossseed.Behavior
	CrossSeedWarning string
	HashSources      hashsource.Registry
	Config           config.Config
	DoctorDeps       doctor.Deps
	Maintenance      MaintenanceClient
	Now              func() time.Time
}

// inspect et inspectionBudget remplacent, dans les tests seulement, l'examen d'un lien et le budget de ListLinks.
type service struct {
	deps             Deps
	inspect          func(store.Link) repair.Inspection
	inspectionBudget time.Duration
}

// New construit le Service sur ses dépendances.
func New(deps Deps) Service {
	return &service{deps: deps}
}

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
	return pipeline.Run(ctx, stage, svc.deps.Stages)
}

// checkStageDeps refuse proprement une étape dont un composant manque, au lieu d'échouer au milieu.
func (svc *service) checkStageDeps(stage pipeline.Stage) error {
	stages := svc.deps.Stages
	switch {
	case stage == pipeline.StageMatch && stages.Provider == nil, stage == pipeline.StageVerify && stages.Fetcher == nil:
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
