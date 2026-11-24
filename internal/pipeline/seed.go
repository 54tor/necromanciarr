package pipeline

import (
	"context"
	"errors"
	"time"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/store"
)

// Erreurs typées de Seed.
var (
	ErrNoTorrentClient = errors.New("pipeline: no torrent client configured")
	// ErrClientReadOnly : le client est déclaré readonly, aucun torrent n'est ajouté et l'étape s'arrête.
	ErrClientReadOnly = errors.New("pipeline: torrent client is read-only")
)

// Raisons propres à Seed.
const (
	ReasonNotLinked              = "not_linked"
	ReasonClientReadOnly         = "client_read_only"
	ReasonAddFailed              = "add_failed"
	ReasonRecheckFailed          = "recheck_failed"
	ReasonResumeFailed           = "resume_failed"
	ReasonIncompleteAfterRecheck = "incomplete_after_recheck"
	ReasonPausedIncomplete       = "paused_incomplete"
	ReasonCrossSeedNotifyFailed  = "crossseed_notify_failed"
)

// Avertissements consignés sur la proposition (texte fixe, sans URL ni secret).
const (
	warningIncompleteAfterRecheck = "left paused: the client did not reach 100% after its recheck"
	warningCrossSeedNotifyFailed  = "cross-seed notification failed: the daemon will not search for this torrent"
)

// defaultSeedRecheckTimeout couvre le recontrôle d'un gros torrent sur disque lent ; au-delà, il reste en pause.
const defaultSeedRecheckTimeout = time.Hour

// runSeed ajoute au client, une proposition liée à la fois, chaque torrent toujours arrêté ; seul un torrent complet
// (notre vérification à 100 %, confirmée par le client) est repris. Un torrent incomplet reste en pause : le client
// réécrirait sinon des pièces à cheval sur un fichier lié de la bibliothèque.
func runSeed(ctx context.Context, deps Deps) (Result, error) {
	var result Result
	if deps.CrossSeedWarning != "" {
		result.Warnings = append(result.Warnings, deps.CrossSeedWarning)
	}
	if deps.Client == nil {
		return result, ErrNoTorrentClient
	}
	work, err := loadAcceptedWork(ctx, deps)
	if err != nil {
		return result, err
	}
	if err := work.checkGroups(); err != nil {
		return result, err
	}
	run := seedRun{deps: deps, result: &result}
	for _, proposal := range work.proposals {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		candidate, reason := work.eligibility(proposal)
		progress := work.progress[proposal.ID]
		if reason == "" && progress.Step != store.StepLinked {
			reason = ReasonNotLinked
		}
		if reason != "" {
			result.Skipped++
			result.count(reason, 1)
			continue
		}
		if err := run.seedProposal(ctx, proposal, candidate, progress.LinkRoot); err != nil {
			return result, err
		}
	}
	return result, nil
}

type seedRun struct {
	deps   Deps
	result *Result
}

func (run seedRun) seedProposal(ctx context.Context, proposal store.Proposal, candidate store.Candidate, linkRoot string) error {
	raw, err := run.deps.Store.CandidateTorrent(ctx, candidate.ID)
	if err != nil {
		return err
	}
	complete, err := run.verifiedComplete(ctx, proposal, candidate.ID)
	if err != nil {
		return err
	}
	skipChecking := complete && run.deps.SkipRecheck
	if err := run.addStopped(ctx, raw, candidate.Infohash, linkRoot, skipChecking); err != nil {
		var refused refusal
		if errors.As(err, &refused) {
			return failProposal(ctx, run.deps, run.result, proposal.ID, string(refused))
		}
		return err
	}
	if !complete {
		run.result.count(ReasonPausedIncomplete, 1)
		return run.finish(ctx, proposal.ID, store.StepSeededPausedIncomplete, linkRoot)
	}
	started, reason := run.startComplete(ctx, candidate.Infohash, skipChecking)
	if reason != "" {
		return failProposal(ctx, run.deps, run.result, proposal.ID, reason)
	}
	if !started {
		run.result.count(ReasonIncompleteAfterRecheck, 1)
		if err := run.deps.Store.AddProposalWarning(ctx, proposal.ID, warningIncompleteAfterRecheck); err != nil {
			return err
		}
		return run.finish(ctx, proposal.ID, store.StepSeededPausedIncomplete, linkRoot)
	}
	if err := run.finish(ctx, proposal.ID, store.StepSeeded, linkRoot); err != nil {
		return err
	}
	return run.notify(ctx, proposal.ID, candidate.Infohash)
}

// verifiedComplete : seul un torrent complet dont NOTRE vérification a couvert toutes les pièces peut être repris ;
// l'import skipRecheck de cross-seed ne l'emporte jamais sur ce principe.
func (run seedRun) verifiedComplete(ctx context.Context, proposal store.Proposal, candidateID int64) (bool, error) {
	isCompleteKind := proposal.Kind == store.ProposalFull || proposal.Kind == store.ProposalCrossSeed
	if !isCompleteKind {
		return false, nil
	}
	verification, found, err := run.deps.Store.LoadVerification(ctx, candidateID)
	if err != nil {
		return false, err
	}
	return found && verification.TotalPieces > 0 && verification.VerifiedPieces == verification.TotalPieces, nil
}

// addStopped ajoute le torrent arrêté s'il n'est pas déjà dans le client (reprise après un arrêt) ; le chemin envoyé est
// celui que le client voit. Un client readonly arrête l'étape.
func (run seedRun) addStopped(ctx context.Context, raw []byte, infohash, linkRoot string, skipChecking bool) error {
	_, err := run.deps.Client.Progress(ctx, infohash)
	if err == nil {
		return nil
	}
	if !errors.Is(err, qbit.ErrNotFound) {
		return refusal(ReasonAddFailed)
	}
	options := qbit.AddOptions{Category: run.deps.Category, Tags: run.deps.Tags, Start: false, SkipChecking: skipChecking}
	err = run.deps.Client.Add(ctx, raw, run.clientPath(linkRoot), options)
	switch {
	case errors.Is(err, qbit.ErrReadOnly):
		run.result.count(ReasonClientReadOnly, 1)
		return ErrClientReadOnly
	case err != nil:
		return refusal(ReasonAddFailed)
	}
	return nil
}

// refusal est un refus nommé par sa raison : la proposition passe à failed et l'étape continue.
type refusal string

func (reason refusal) Error() string { return "pipeline: refused: " + string(reason) }

// clientPath traduit le dossier du torrent vers la vue du client ; sans règle qui le couvre (linker.ErrNoRule), il
// voit le même chemin.
func (run seedRun) clientPath(linkRoot string) string {
	if run.deps.ClientMapper == nil {
		return linkRoot
	}
	translated, err := run.deps.ClientMapper.Translate(linkRoot)
	if err != nil {
		return linkRoot
	}
	return translated
}

// startComplete reprend le torrent seulement si le client le voit à 100 % : sans recontrôle, l'avancement doit déjà
// valoir 1 ; sinon (ou à défaut) la garde recontrôle et ne reprend que s'il est complet.
func (run seedRun) startComplete(ctx context.Context, infohash string, skipChecking bool) (bool, string) {
	if skipChecking {
		progress, err := run.deps.Client.Progress(ctx, infohash)
		if err == nil && progress == 1 {
			if err := run.deps.Client.Resume(ctx, infohash); err != nil {
				return false, ReasonResumeFailed
			}
			return true, ""
		}
	}
	timeout := run.deps.RecheckTimeout
	if timeout <= 0 {
		timeout = defaultSeedRecheckTimeout
	}
	guard, err := run.deps.Client.RecheckAndGuard(ctx, infohash, timeout)
	if err != nil {
		return false, ReasonRecheckFailed
	}
	return guard.Complete && guard.Resumed, ""
}

func (run seedRun) finish(ctx context.Context, proposalID int64, step store.ProposalStep, linkRoot string) error {
	if err := run.deps.Store.ApplyProposal(ctx, proposalID, step, linkRoot, run.deps.now()); err != nil {
		return err
	}
	run.result.Processed++
	if step == store.StepSeeded {
		run.result.Seeded++
	} else {
		run.result.SeededPausedIncomplete++
	}
	return nil
}

// notify prévient cross-seed en mode Delegate, au mieux : un échec est un avertissement, jamais un échec du seed. En
// Native, AcceptGroup a déjà accepté les autres torrents ; en Disabled, rien.
func (run seedRun) notify(ctx context.Context, proposalID int64, infohash string) error {
	if run.deps.CrossSeed != crossseed.Delegate || run.deps.Notifier == nil {
		return nil
	}
	if err := run.deps.Notifier.Notify(ctx, infohash); err != nil {
		run.result.count(ReasonCrossSeedNotifyFailed, 1)
		return run.deps.Store.AddProposalWarning(context.WithoutCancel(ctx), proposalID, warningCrossSeedNotifyFailed)
	}
	return nil
}
