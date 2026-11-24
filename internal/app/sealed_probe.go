package app

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"necromanciarr/internal/doctor"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// openStoreProbe lit les blocs scellés sur le store déjà ouvert par serve.
type openStoreProbe struct {
	store *store.Store
}

func (probe openStoreProbe) EachSealedTorrent(ctx context.Context, limit int, maxBytes int64, visit func(doctor.SealedTorrent) error) (int, error) {
	return eachSealed(ctx, probe.store, limit, maxBytes, visit)
}

// pathProbe ouvre la base en lecture seule le temps du contrôle (diagnostic de la CLI, sans store ouvert) : ni
// création, ni mise à niveau, ni écriture. Une base absente n'a aucun bloc.
type pathProbe struct {
	databasePath string
}

func (probe pathProbe) EachSealedTorrent(ctx context.Context, limit int, maxBytes int64, visit func(doctor.SealedTorrent) error) (int, error) {
	if _, err := os.Stat(probe.databasePath); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	readOnly, err := store.OpenReadOnly(probe.databasePath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = readOnly.Close() }()
	return eachSealed(ctx, readOnly, limit, maxBytes, visit)
}

// eachSealed remet chaque bloc avec sa liaison reprise de la ligne du candidat, jamais de la configuration.
func eachSealed(ctx context.Context, sealedStore *store.Store, limit int, maxBytes int64, visit func(doctor.SealedTorrent) error) (int, error) {
	return sealedStore.EachSealedTorrent(ctx, limit, maxBytes, func(torrent store.SealedTorrent) error {
		binding := sources.Binding{Infohash: torrent.Infohash, Tracker: torrent.Tracker, Source: sources.ID(torrent.Source)}
		return visit(doctor.SealedTorrent{CandidateID: torrent.CandidateID, Binding: binding, Blob: torrent.Blob})
	})
}
