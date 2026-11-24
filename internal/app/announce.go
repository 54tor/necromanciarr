package app

import (
	"context"

	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// SealedClearer efface les .torrent scellés des trackers qu'une entrée trackers couvre ; *store.Store convient.
type SealedClearer interface {
	ClearSealedForTrackers(ctx context.Context, trackers []string) (int, error)
}

// announces résout les annonces d'après la section trackers courante : Link, Seed et chaque lecture en partent.
func (svc *service) announces() sources.Resolver {
	return sources.NewResolver(svc.deps.Config.Trackers)
}

// clearCoveredSealed efface le .torrent scellé de chaque candidat qu'une entrée trackers couvre désormais (la
// configuration gagne, le scellé ne sert plus) et met à jour HasSealed de chaque candidat lu. Les valeurs de tracker
// passées au store sont celles stockées, comparées octet pour octet ; la normalisation reste dans la configuration.
func clearCoveredSealed(ctx context.Context, clearer SealedClearer, resolver sources.Resolver, candidates []store.Candidate) error {
	var covered []string
	for position, candidate := range candidates {
		if !candidate.HasSealed || resolver.Resolve(candidate.Tracker, true).Source != sources.AnnounceConfig {
			continue
		}
		covered = append(covered, candidate.Tracker)
		candidates[position].HasSealed = false
	}
	if len(covered) == 0 {
		return nil
	}
	_, err := clearer.ClearSealedForTrackers(ctx, covered)
	return err
}

// withAnnounce complète la vue d'une proposition de torrent : seedable, announce_source et, sans annonce,
// l'avertissement no_announce (privé ou public), recalculés à chaque lecture depuis la section trackers courante.
func (state snapshot) withAnnounce(translator i18n.Translator, view Proposal) Proposal {
	if !pipeline.IsTorrentProposal(view.Kind) {
		return view
	}
	candidate := state.candidates[view.CandidateID]
	source := pipeline.AnnounceSourceOf(state.announces, candidate)
	view.Details = view.Details.WithAnnounce(source)
	if source == sources.AnnounceNone {
		view.Warnings = append(view.Warnings, translator.Rendered(pipeline.NoAnnounceWarning(candidate.Tracker)))
	}
	return view
}
