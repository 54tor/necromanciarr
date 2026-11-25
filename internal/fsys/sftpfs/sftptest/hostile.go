package sftptest

import (
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
)

// Drapeaux SSH_FXF_* d'une ouverture, tels que Request.Flags les enregistre.
const (
	FlagRead      uint32 = 0x01
	FlagWrite     uint32 = 0x02
	FlagAppend    uint32 = 0x04
	FlagCreate    uint32 = 0x08
	FlagTruncate  uint32 = 0x10
	FlagExclusive uint32 = 0x20
)

// Entry est une entrée factice servie telle quelle, nom brut compris (« a/b », « .. », NUL) : un serveur hostile.
type Entry struct {
	Name string
	Size int64
	Mode os.FileMode
}

// WithListing remplace la liste du dossier dir (chemin du client) par entries, dans cet ordre ; Stat et Lstat de
// dir/<nom> rendent l'entrée factice de ce nom.
func WithListing(dir string, entries ...Entry) Option {
	return func(current *settings) { current.listings[path.Clean(dir)] = entries }
}

// WithReadlinkTarget fait rendre target à Readlink de linkPath (chemin du client), sans lien sur le disque.
func WithReadlinkTarget(linkPath, target string) Option {
	return func(current *settings) { current.readlinkTargets[path.Clean(linkPath)] = target }
}

// WithStatVFS modifie la réponse statvfs@openssh.com avant son envoi (valeurs hostiles, fsid nul).
func WithStatVFS(mutate func(*sftp.StatVFS)) Option {
	return func(current *settings) { current.statVFSMutator = mutate }
}

// fakeEntry rend l'entrée factice clientPath de WithListing, s'il y en a une.
func (handler *fileHandler) fakeEntry(clientPath string) (os.FileInfo, bool) {
	cleaned := path.Clean(clientPath)
	for _, entry := range handler.server.settings.listings[path.Dir(cleaned)] {
		if entry.Name == path.Base(cleaned) {
			return fakeInfo{entry: entry}, true
		}
	}
	return nil, false
}

func (handler *fileHandler) fakeListing(clientDir string) (fileInfos, bool) {
	entries, found := handler.server.settings.listings[path.Clean(clientDir)]
	if !found {
		return nil, false
	}
	infos := make(fileInfos, 0, len(entries))
	for _, entry := range entries {
		infos = append(infos, fakeInfo{entry: entry})
	}
	return infos, true
}

// fakeInfo sert une Entry comme os.FileInfo : pkg/sftp envoie Name() sans le retoucher.
type fakeInfo struct{ entry Entry }

func (info fakeInfo) Name() string       { return info.entry.Name }
func (info fakeInfo) Size() int64        { return info.entry.Size }
func (info fakeInfo) Mode() os.FileMode  { return info.entry.Mode }
func (info fakeInfo) ModTime() time.Time { return time.Unix(0, 0) }
func (info fakeInfo) IsDir() bool        { return info.entry.Mode.IsDir() }
func (info fakeInfo) Sys() any           { return nil }
