package qbit

import (
	"context"
	"fmt"
	"strings"

	"necromanciarr/internal/repair"
	"necromanciarr/internal/torrentcheck"
)

const torrentPathSeparator = "/"

// TorrentForPath satisfait repair.TorrentSource : premier torrent (ordre des hashes) dont un fichier est path,
// relu par export et torrentcheck.Parse. N'importe lequel convient : repair vérifie le fichier contre ses pièces.
func (client *Client) TorrentForPath(ctx context.Context, filePath string) (repair.TorrentRef, bool, error) {
	covering, err := client.torrentsCovering(ctx, filePath)
	if err != nil || len(covering) == 0 {
		return repair.TorrentRef{}, false, err
	}
	match := covering[0]
	raw, err := client.ExportTorrent(ctx, match.hash)
	if err != nil {
		return repair.TorrentRef{}, false, err
	}
	parsed, err := torrentcheck.Parse(raw)
	if err != nil {
		return repair.TorrentRef{}, false, fmt.Errorf("%w: export of %s: %w", ErrBadResponse, match.hash, err)
	}
	if !strings.EqualFold(parsed.InfoHash, match.hash) {
		return repair.TorrentRef{}, false, fmt.Errorf("%w: export of %s has another info hash", ErrBadResponse, match.hash)
	}
	fileIndex := fileIndexOf(parsed, match.relativeName)
	if fileIndex < 0 {
		return repair.TorrentRef{}, false, nil
	}
	return repair.TorrentRef{Torrent: parsed, FileIndex: fileIndex}, true, nil
}

// fileIndexOf accepte le nom avec dossier racine (disposition Original) ou sans (fichier seul, NoSubfolder).
func fileIndexOf(parsed *torrentcheck.Torrent, relativeName string) int {
	for fileIndex, file := range parsed.Files {
		if file.Path == relativeName || parsed.Root+torrentPathSeparator+file.Path == relativeName {
			return fileIndex
		}
	}
	return -1
}
