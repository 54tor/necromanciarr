package pipeline

import (
	"context"
	"errors"
	"fmt"

	"necromanciarr/internal/store"
)

// ErrGroupInconsistent signale une proposition acceptée rattachée à un autre groupe que son candidat : rien n'est
// appliqué tant que le passage de groupe (Match ou Verify) n'a pas resynchronisé les propositions.
var ErrGroupInconsistent = errors.New("pipeline: proposal group differs from its candidate group")

// Raisons communes à Link et Seed.
const (
	ReasonNotATorrentProposal    = "not_a_torrent_proposal"
	ReasonCandidateNotVerified   = "candidate_not_verified"
	ReasonStaleGeneration        = "stale_generation"
	ReasonCandidateAlreadyLinked = "candidate_already_linked"
)

// torrentProposalKinds sont les natures qui lient puis ajoutent un torrent ; un litige ne s'accepte pas (Choose).
var torrentProposalKinds = map[store.ProposalKind]bool{
	store.ProposalFull: true, store.ProposalPartialAnnex: true, store.ProposalPartialPack: true, store.ProposalCrossSeed: true,
}

// IsTorrentProposal dit si la nature de proposition lie et ajoute un torrent (full, partial_annex, partial_pack, cross_seed).
func IsTorrentProposal(kind store.ProposalKind) bool {
	return torrentProposalKinds[kind]
}

// acceptedWork est l'entrée de Link et Seed : propositions acceptées (triées par id), candidat vérifié par id et étapes.
type acceptedWork struct {
	proposals  []store.Proposal
	candidates map[int64]store.Candidate
	progress   map[int64]store.ProposalProgress
}

func loadAcceptedWork(ctx context.Context, deps Deps) (acceptedWork, error) {
	proposals, err := deps.Store.ListProposals(ctx, store.ProposalAccepted)
	if err != nil {
		return acceptedWork{}, err
	}
	verified, err := deps.Store.ListCandidatesByState(ctx, store.CandidateVerified)
	if err != nil {
		return acceptedWork{}, err
	}
	progress, err := deps.Store.ListProposalProgress(ctx)
	if err != nil {
		return acceptedWork{}, err
	}
	candidates := make(map[int64]store.Candidate, len(verified))
	for _, candidate := range verified {
		candidates[candidate.ID] = candidate
	}
	return acceptedWork{proposals: proposals, candidates: candidates, progress: progress}, nil
}

// checkGroups refuse toute l'étape si une proposition acceptée n'a pas le groupe de son candidat.
func (work acceptedWork) checkGroups() error {
	for _, proposal := range work.proposals {
		candidate, known := work.candidates[proposal.CandidateID]
		if known && proposal.GroupID != GroupOf(candidate) {
			return fmt.Errorf("%w: proposal %d in group %d, candidate %d in group %d", ErrGroupInconsistent,
				proposal.ID, proposal.GroupID, candidate.ID, GroupOf(candidate))
		}
	}
	return nil
}

// eligibility renvoie le candidat de la proposition, ou la raison qui l'écarte : nature sans torrent, candidat non
// vérifié, génération périmée.
func (work acceptedWork) eligibility(proposal store.Proposal) (store.Candidate, string) {
	if !IsTorrentProposal(proposal.Kind) {
		return store.Candidate{}, ReasonNotATorrentProposal
	}
	candidate, known := work.candidates[proposal.CandidateID]
	if !known {
		return store.Candidate{}, ReasonCandidateNotVerified
	}
	if proposal.Generation != candidate.Generation {
		return store.Candidate{}, ReasonStaleGeneration
	}
	return candidate, ""
}

// failProposal passe la proposition à failed avec la raison en avertissement ; l'opérateur peut la réaccepter.
func failProposal(ctx context.Context, deps Deps, result *Result, proposalID int64, reason string) error {
	result.Failed++
	result.count(reason, 1)
	if err := deps.Store.SetProposalDecision(ctx, proposalID, store.ProposalFailed, deps.now()); err != nil {
		return err
	}
	return deps.Store.AddProposalWarning(ctx, proposalID, failureWarning(reason))
}

// linkKindActions dit à l'opérateur, pour chaque refus de sorte de lien, la cause et quoi changer ; aucun repli vers
// une autre sorte n'est jamais tenté d'office.
var linkKindActions = map[string]string{
	ReasonCrossDevice: "hardlink impossible entre deux montages : placer linkDirs sous le même point de montage que " +
		"la bibliothèque, ou choisir linkType symlink/reflink",
	ReasonNoLinkDirOnSameDevice: "aucun linkDirs sur le même système de fichiers que la bibliothèque : placer linkDirs " +
		"sous le même point de montage que la bibliothèque, ou choisir linkType symlink",
	ReasonLinkKindRefused: "le système de fichiers de linkDirs refuse cette sorte de lien : choisir un autre linkType " +
		"(le contrôle doctor des liens indique les sortes acceptées)",
}

// failureWarning est l'avertissement laissé sur une proposition en échec : la raison, puis l'action attendue de
// l'opérateur quand elle est connue.
func failureWarning(reason string) string {
	warning := "application failed: " + reason
	if action, known := linkKindActions[reason]; known {
		warning += " (" + action + ")"
	}
	return warning
}

// linkedCandidates marque chaque candidat dont une proposition acceptée a déjà une étape : une seconde proposition du
// même torrent n'est jamais liée une seconde fois.
func (work acceptedWork) linkedCandidates() map[int64]bool {
	linked := map[int64]bool{}
	for _, proposal := range work.proposals {
		if work.progress[proposal.ID].Step != "" {
			linked[proposal.CandidateID] = true
		}
	}
	return linked
}
