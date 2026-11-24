package pipeline

import (
	"context"
	"errors"

	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// announcedTorrent construit en mémoire le .torrent remis au client : la copie assainie stockée (info intact), avec
// l'annonce de la configuration, sinon celle du .torrent scellé de l'indexeur. Son infohash, relu, doit être celui du
// candidat. Le résultat ne va qu'à Client.Add : ni base, ni journal, ni résultat d'étape.
func (run seedRun) announcedTorrent(ctx context.Context, candidate store.Candidate) ([]byte, error) {
	resolution := run.deps.Announces.Resolve(candidate.Tracker, candidate.HasSealed)
	tiers := resolution.Tiers
	switch resolution.Source {
	case sources.AnnounceNone:
		return nil, refusal(ReasonNoAnnounce)
	case sources.AnnounceIndexer:
		indexerTiers, err := run.indexerTiers(ctx, candidate)
		if err != nil {
			return nil, err
		}
		tiers = indexerTiers
	}
	stored, err := run.deps.Store.CandidateTorrent(ctx, candidate.ID)
	if err != nil {
		return nil, err
	}
	injected, err := sources.InjectAnnounce(stored, tiers)
	switch {
	case errors.Is(err, sources.ErrInfohashChanged):
		return nil, refusal(ReasonInfohashChangedAfterInject)
	case err != nil:
		return nil, refusal(ReasonInvalidTorrent)
	}
	if !hasInfohash(injected, candidate.Infohash) {
		clear(injected)
		return nil, refusal(ReasonInfohashChangedAfterInject)
	}
	return injected, nil
}

// indexerTiers lit l'annonce du .torrent scellé : liaison reprise de la ligne du candidat, jamais de la configuration ;
// le clair est effacé dès l'annonce lue, et ses passkeys supposées rejoignent les valeurs masquées de l'étape.
func (run seedRun) indexerTiers(ctx context.Context, candidate store.Candidate) (sources.Tiers, error) {
	if run.deps.Sealer == nil {
		return nil, refusal(ReasonSecretKeyMissing)
	}
	blob, found, err := run.deps.Store.CandidateSealed(ctx, candidate.ID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, refusal(ReasonNoAnnounce)
	}
	binding := sources.Binding{Infohash: candidate.Infohash, Tracker: candidate.Tracker, Source: sources.ID(candidate.Source)}
	plain, err := run.deps.Sealer.Unseal(blob, binding)
	if err != nil {
		return nil, refusal(ReasonSealedTorrentUnreadable)
	}
	defer clear(plain)
	if !hasInfohash(plain, candidate.Infohash) {
		return nil, refusal(ReasonInfohashChangedAfterInject)
	}
	tiers, err := sources.AnnounceTiers(plain)
	switch {
	case errIsNoAnnounce(err):
		return nil, refusal(ReasonNoAnnounce)
	case err != nil:
		return nil, refusal(ReasonInvalidTorrent)
	}
	*run.discovered = append(*run.discovered, sources.DiscoveredSecretValues(tiers)...)
	return tiers, nil
}
