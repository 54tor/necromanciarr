package app

import (
	"context"
	"log/slog"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// SealedClearer efface les .torrent scellés des trackers qu'une entrée trackers couvre ; *store.Store convient.
type SealedClearer interface {
	ClearSealedForTrackers(ctx context.Context, trackers []string) (int, error)
}

// LogCodeSealedClearFailed est le code stable de la ligne de journal d'un effacement de scellés en échec à la lecture.
const LogCodeSealedClearFailed = "sealed_clear_failed"

// logLine écrit une ligne codée, traduite, dont chaque paramètre est masqué par les secrets connus de la configuration.
func (svc *service) logLine(ctx context.Context, level slog.Level, code string, key i18n.Key, params map[string]string) {
	logger := svc.deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	redacted := make(map[string]string, len(params))
	for name, value := range params {
		redacted[name] = config.RedactText(value, svc.deps.Config.KnownSecrets()...)
	}
	logger.LogAttrs(ctx, level, svc.deps.Translator.Text(key, redacted), slog.String("code", code))
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
		view.Warnings = append(view.Warnings, translator.Rendered(pipeline.NoAnnounceWarning(trackerLabel(translator, candidate.Tracker))))
	}
	return view
}

// trackerLabel nomme le tracker dans un avertissement ; un tracker vide (fournisseur sans tracker) devient « (inconnu) ».
func trackerLabel(translator i18n.Translator, tracker string) string {
	if strings.TrimSpace(tracker) == "" {
		return translator.Text(i18n.KeyWarningNoAnnounceUnknownTracker, nil)
	}
	return tracker
}
