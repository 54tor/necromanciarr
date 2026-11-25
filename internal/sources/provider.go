package sources

import (
	"context"
	"errors"

	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
)

// ErrTorrentTooLarge : le .torrent dépasse la taille conservée (store.MaxTorrentBytes) ; testé avant l'assainissement,
// qui le refuserait sous une autre raison.
var ErrTorrentTooLarge = errors.New("sources: torrent exceeds the size limit")

// ProviderSource est la part de libraryprovider.Client que la chaîne utilise ; *libraryprovider.Client convient sans
// adaptation, son code ne change pas.
type ProviderSource interface {
	Search(ctx context.Context, query libraryprovider.Query) ([]libraryprovider.Release, error)
	Torrent(ctx context.Context, infohash, tracker string) ([]byte, error)
	Nfos(ctx context.Context, infohash, tracker string) ([]libraryprovider.Nfo, error)
}

// fetchFromProvider télécharge le .torrent du fournisseur et ne garde que la copie assainie : le fournisseur sert déjà
// des .torrent sans annonce, rien n'est scellé (Sealed nil). Les erreurs du fournisseur passent telles quelles.
func fetchFromProvider(ctx context.Context, provider ProviderSource, release Release) (Fetched, error) {
	raw, err := provider.Torrent(ctx, release.Infohash, release.Tracker)
	if err != nil {
		return Fetched{}, err
	}
	if len(raw) > store.MaxTorrentBytes {
		return Fetched{}, ErrTorrentTooLarge
	}
	sanitized, err := Sanitize(raw)
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Source: ProviderID, Sanitized: sanitized}, nil
}
