package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

// Erreurs typées des actions dedupe et repair.
var (
	// ErrNotAccepted : seule une proposition déjà acceptée par l'opérateur s'applique (jamais d'application d'office).
	ErrNotAccepted = errors.New("app: the proposal must be accepted first")
	// ErrNotActionable : la proposition est listée pour information (ambiguë, perdue, sans sorte de lien) ; elle ne
	// s'accepte ni ne s'applique.
	ErrNotActionable = errors.New("app: this proposal is listed for information only")
	// ErrWrongProposalKind : l'action dédiée ne traite pas cette nature de proposition.
	ErrWrongProposalKind = errors.New("app: wrong proposal kind for this action")
	// ErrDirectoryInLibrary : un dossier à traiter recouvre une racine de la bibliothèque, qui n'est jamais modifiée.
	ErrDirectoryInLibrary = errors.New("app: directory overlaps a library root")
	// ErrRecheckUnavailable : la réparation par présomption exige un recontrôle, et les torrents qui couvrent le lien
	// n'ont pas pu être listés ; rien n'est remplacé.
	ErrRecheckUnavailable = errors.New("app: the torrents covering this link cannot be listed, repair refused")
)

// Avertissements posés sur une proposition dedupe ou repair appliquée.
const (
	// WarningPausedIncompleteAfterRecheck : après le remplacement, le recontrôle du client a trouvé le torrent
	// incomplet ; il reste en pause et n'est jamais repris par necromanciarr.
	WarningPausedIncompleteAfterRecheck = "paused_incomplete_after_recheck"
	// WarningRecheckFailed : le recontrôle a échoué ; le client a laissé le torrent en pause.
	WarningRecheckFailed = "recheck_failed"
	// WarningNoClientRecheck : dedupe remplacera le fichier par un lien dur ou symbolique vers la bibliothèque sans
	// recontrôle du client (aucun client configuré, ou recheckAfter faux) ; un torrent qui tourne pourrait y écrire.
	WarningNoClientRecheck = "warning.no_client_recheck"
	// legacyNoClientRecheck : valeur courte écrite par les versions précédentes dans details.warnings.
	legacyNoClientRecheck = "no_client_recheck"
	// WarningLinkNotRecorded : le lien est posé mais la base ne l'a pas enregistré.
	WarningLinkNotRecorded = "link_not_recorded"
)

// lockNameDedupe et lockNameRepair nomment le verrou des étapes tenu par ces actions.
const (
	lockNameDedupe = "dedupe"
	lockNameRepair = "repair"
)

// MaintenanceResult rend compte d'une application dedupe ou repair : LinkTarget est la cible écrite (ou qui le
// serait en DryRun), BytesRead les octets lus pour la preuve, Warnings les avertissements ajoutés.
type MaintenanceResult struct {
	ProposalID int64     `json:"proposal_id"`
	Applied    bool      `json:"applied"`
	DryRun     bool      `json:"dry_run"`
	LinkTarget string    `json:"link_target,omitempty"`
	BytesRead  int64     `json:"bytes_read"`
	Warnings   []Warning `json:"warnings,omitempty"`
	// stored garde les messages tels qu'ils seront enregistrés sur la proposition.
	stored []i18n.Message
}

// withWarnings pose les avertissements : rendus dans la langue pour la sortie, bruts pour l'enregistrement.
func (result MaintenanceResult) withWarnings(translator i18n.Translator, messages []i18n.Message) MaintenanceResult {
	result.stored = messages
	result.Warnings = nil
	if len(messages) > 0 {
		result.Warnings = warningViews(translator, messages)
	}
	return result
}

// maintenanceDetails est le détail d'une proposition dedupe ou repair : key l'identifie d'une exécution à l'autre,
// signature change quand les fichiers décrits changent.
type maintenanceDetails interface {
	identity() (key, signature string)
}

// isMaintenanceKind : propositions sans torrent, appliquées par leurs actions dédiées.
func isMaintenanceKind(kind store.ProposalKind) bool {
	return kind == store.ProposalDedupe || kind == store.ProposalRepair
}

func isOpenDecision(decision store.ProposalDecision) bool {
	return decision == store.ProposalPending || decision == store.ProposalAccepted
}

// storedMaintenance est une proposition enregistrée avec son détail décodé.
type storedMaintenance[Details maintenanceDetails] struct {
	proposal store.Proposal
	details  Details
}

// persistMaintenance enregistre une proposition par entrée, sans doublon : une proposition non close de même clé et de
// même signature est reprise telle quelle (y compris rejetée) ; une proposition ouverte dont les fichiers ont changé,
// ou absente de cette exécution, passe à superseded. Le résultat suit l'ordre des entrées.
// outOfScope dit qu'une proposition connue n'était pas examinable par cette exécution (repair : dossier d'un autre
// scan) : absente, elle n'est pas close pour autant.
func persistMaintenance[Details maintenanceDetails](ctx context.Context, svc *service, kind store.ProposalKind, entries []Details, warningsOf func(Details) []string, outOfScope func(Details) bool) ([]storedMaintenance[Details], error) {
	existing, err := loadMaintenanceOfKind[Details](ctx, svc, kind)
	if err != nil {
		return nil, err
	}
	latestByKey := map[string]storedMaintenance[Details]{}
	for _, known := range existing {
		key, _ := known.details.identity()
		latestByKey[key] = known
	}
	seenKeys := map[string]bool{}
	persisted := make([]storedMaintenance[Details], 0, len(entries))
	for _, entry := range entries {
		key, signature := entry.identity()
		seenKeys[key] = true
		known, found := latestByKey[key]
		if found {
			_, knownSignature := known.details.identity()
			if knownSignature == signature {
				persisted = append(persisted, known)
				continue
			}
			if err := svc.supersedeIfOpen(ctx, known.proposal); err != nil {
				return nil, err
			}
		}
		created, err := svc.createMaintenance(ctx, kind, entry, messagesOfCodes(warningsOf(entry)))
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, storedMaintenance[Details]{proposal: created, details: entry})
	}
	for key, known := range latestByKey {
		if !seenKeys[key] && !outOfScope(known.details) {
			if err := svc.supersedeIfOpen(ctx, known.proposal); err != nil {
				return nil, err
			}
		}
	}
	return persisted, nil
}

// loadMaintenanceOfKind renvoie, triées par id, les propositions non closes de la nature avec leur détail.
func loadMaintenanceOfKind[Details maintenanceDetails](ctx context.Context, svc *service, kind store.ProposalKind) ([]storedMaintenance[Details], error) {
	proposals, err := svc.deps.Store.ListProposals(ctx, "")
	if err != nil {
		return nil, err
	}
	var loaded []storedMaintenance[Details]
	for _, proposal := range proposals {
		if proposal.Kind != kind || proposal.Decision == store.ProposalSuperseded {
			continue
		}
		var details Details
		if err := json.Unmarshal(proposal.Details, &details); err != nil {
			return nil, fmt.Errorf("app: proposal %d details: %w", proposal.ID, err)
		}
		loaded = append(loaded, storedMaintenance[Details]{proposal: proposal, details: details})
	}
	return loaded, nil
}

func (svc *service) supersedeIfOpen(ctx context.Context, proposal store.Proposal) error {
	if !isOpenDecision(proposal.Decision) {
		return nil
	}
	return svc.deps.Store.SetProposalDecision(ctx, proposal.ID, store.ProposalSuperseded, svc.now())
}

func (svc *service) createMaintenance(ctx context.Context, kind store.ProposalKind, details any, warnings []i18n.Message) (store.Proposal, error) {
	encoded, err := json.Marshal(details)
	if err != nil {
		return store.Proposal{}, err
	}
	proposal := store.Proposal{Kind: kind, Decision: store.ProposalPending, Warnings: warnings, Details: encoded}
	proposal.ID, err = svc.deps.Store.CreateProposal(ctx, proposal, svc.now())
	return proposal, err
}

// loadAcceptedMaintenance renvoie le détail de la proposition, de la nature attendue et applicable, contrôlés avant
// toute écriture. Elle doit être acceptée ; avec acceptPending, une proposition en attente est d'abord acceptée (une
// transaction du store, qui refuse aussi une décision changée entre-temps) ; avec dryRun, en attente suffit et rien
// n'est décidé.
func loadAcceptedMaintenance[Details maintenanceDetails](ctx context.Context, svc *service, proposalID int64, kind store.ProposalKind, actionable func(Details) bool, options []DecisionOption) (Details, error) {
	var details Details
	state, err := svc.loadSnapshot(ctx)
	if err != nil {
		return details, err
	}
	proposal, err := state.proposal(proposalID)
	if err != nil {
		return details, err
	}
	if proposal.Kind != kind {
		return details, fmt.Errorf("%w: proposal %d is %s, not %s", ErrWrongProposalKind, proposalID, proposal.Kind, kind)
	}
	if err := json.Unmarshal(proposal.Details, &details); err != nil {
		return details, fmt.Errorf("app: proposal %d details: %w", proposalID, err)
	}
	if !actionable(details) {
		return details, ErrNotActionable
	}
	isPending := proposal.Decision == store.ProposalPending
	switch {
	case proposal.Decision == store.ProposalAccepted, isPending && slices.Contains(options, DryRun):
		return details, nil
	case isPending && slices.Contains(options, AcceptPending):
		return details, svc.accept(ctx, proposal)
	}
	return details, fmt.Errorf("%w: proposal %d is %s", ErrNotAccepted, proposalID, proposal.Decision)
}

// acceptMaintenance accepte une proposition dedupe ou repair applicable ; une proposition failed est remise en jeu.
func (svc *service) acceptMaintenance(ctx context.Context, proposal store.Proposal) error {
	var flags struct {
		Actionable bool `json:"actionable"`
	}
	if err := json.Unmarshal(proposal.Details, &flags); err != nil || !flags.Actionable {
		return ErrNotActionable
	}
	return svc.accept(ctx, proposal)
}

// finishMaintenance enregistre l'issue : appliquée avec ses avertissements, périmée (fichiers changés depuis le plan :
// superseded et ErrStaleProposal), sinon failed avec la raison.
func (svc *service) finishMaintenance(ctx context.Context, result *MaintenanceResult, done bool, cause error, staleCauses ...error) error {
	if done {
		if err := svc.deps.Store.SetProposalDecision(ctx, result.ProposalID, store.ProposalApplied, svc.now()); err != nil {
			return err
		}
		result.Applied = true
		return svc.addWarnings(ctx, result.ProposalID, result.stored)
	}
	for _, staleCause := range staleCauses {
		if errors.Is(cause, staleCause) {
			if err := svc.deps.Store.SetProposalDecision(ctx, result.ProposalID, store.ProposalSuperseded, svc.now()); err != nil {
				return errors.Join(cause, err)
			}
			return fmt.Errorf("%w: %w", ErrStaleProposal, cause)
		}
	}
	if err := svc.deps.Store.SetProposalDecision(ctx, result.ProposalID, store.ProposalFailed, svc.now()); err != nil {
		return errors.Join(cause, err)
	}
	return errors.Join(cause, svc.deps.Store.AddProposalWarning(ctx, result.ProposalID, i18n.Message{Key: i18n.KeyWarningApplicationFailedDetail, Params: map[string]string{"detail": cause.Error()}}))
}

// bookkeepingTimeout borne l'écriture en base qui suit un renommage déjà fait, même après une annulation.
const bookkeepingTimeout = 30 * time.Second

// bookkeeping : après un remplacement irréversible sur disque, la décision doit être écrite même si l'opérateur a
// interrompu l'action, sinon la base ne refléterait plus le disque.
func bookkeeping(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), bookkeepingTimeout)
}

func (svc *service) addWarnings(ctx context.Context, proposalID int64, warnings []i18n.Message) error {
	for _, warning := range warnings {
		if err := svc.deps.Store.AddProposalWarning(ctx, proposalID, warning); err != nil {
			return err
		}
	}
	return nil
}

func (svc *service) locked(ctx context.Context, name string, work func(ctx context.Context) error) error {
	return pipeline.WithLock(ctx, svc.deps.Stages, name, work)
}

// checkOutsideLibrary refuse un dossier égal à une racine de la bibliothèque, son ancêtre ou son descendant, chemins
// comparés tels quels puis résolus (un lien symbolique ne contourne pas le contrôle).
func checkOutsideLibrary(directories, libraryRoots []string, refusal error) error {
	for _, directory := range directories {
		for _, libraryRoot := range libraryRoots {
			lexicalOverlap := config.PathsOverlap(directory, libraryRoot)
			if lexicalOverlap || config.PathsOverlap(resolvedPath(directory), resolvedPath(libraryRoot)) {
				return fmt.Errorf("%w: %s", refusal, directory)
			}
		}
	}
	return nil
}

func resolvedPath(path string) string {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	resolved, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return absolutePath
	}
	return resolved
}

// existingDirectories ne garde que les dossiers présents : une racine absente ne doit pas empêcher les autres.
func existingDirectories(directories []string) []string {
	var existing []string
	for _, directory := range directories {
		if info, err := os.Stat(directory); err == nil && info.IsDir() {
			existing = append(existing, directory)
		}
	}
	return existing
}
