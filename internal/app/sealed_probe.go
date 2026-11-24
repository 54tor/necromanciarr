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

// sealedProbe lit les blocs scellés de la base pour le contrôle secret-key ; la liaison vient de la ligne du
// candidat, jamais de la configuration. Une base absente n'a aucun bloc : la sonde ne la crée pas.
type sealedProbe struct {
	databasePath string
}

func (probe sealedProbe) SealedTorrents(ctx context.Context, limit int) ([]doctor.SealedTorrent, error) {
	if _, err := os.Stat(probe.databasePath); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	openedStore, err := store.Open(probe.databasePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = openedStore.Close() }()
	stored, err := openedStore.SealedTorrents(ctx, limit)
	if err != nil {
		return nil, err
	}
	sealed := make([]doctor.SealedTorrent, 0, len(stored))
	for _, torrent := range stored {
		binding := sources.Binding{Infohash: torrent.Infohash, Tracker: torrent.Tracker, Source: sources.ID(torrent.Source)}
		sealed = append(sealed, doctor.SealedTorrent{CandidateID: torrent.CandidateID, Binding: binding, Blob: torrent.Blob})
	}
	return sealed, nil
}
