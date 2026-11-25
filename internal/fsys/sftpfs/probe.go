package sftpfs

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"

	"github.com/pkg/sftp"
)

// Verdicts de la sonde d'ordre des arguments de SSH_FXP_SYMLINK, gardés par session.
const (
	symlinkOrderUnknown int32 = iota
	symlinkOrderVerified
	symlinkOrderRefused
)

const (
	probeNamePrefix = ".necromanciarr-probe-"
	probeTargetTail = "-t"
	probeRandomSize = 16
)

// probeSymlinkOrder sonde une fois par session, dans dir (racine dont le RealPath vient d'être vérifié), l'ordre
// des arguments : lien et cible sont deux noms absolus imprévisibles de dir, si bien que les deux ordres restent
// dans la racine. Le lien doit se lire au nom demandé avec la cible demandée. Les deux noms sont retirés ensuite :
// ce sont des entrées que cette sonde vient de créer dans la racine.
func probeSymlinkOrder(current *session, dir string) (bool, error) {
	if verdict := current.symlinkOrder.Load(); verdict != symlinkOrderUnknown {
		return verdict == symlinkOrderVerified, nil
	}
	random := make([]byte, probeRandomSize)
	if _, err := rand.Read(random); err != nil {
		return false, err
	}
	linkPath := path.Join(dir, probeNamePrefix+hex.EncodeToString(random))
	targetPath := linkPath + probeTargetTail
	isVerified := probeSymlink(current.client, linkPath, targetPath)
	if err := removeProbeEntries(current.client, linkPath, targetPath); err != nil {
		return false, err
	}
	verdict := symlinkOrderRefused
	if isVerified {
		verdict = symlinkOrderVerified
	}
	current.symlinkOrder.Store(verdict)
	return isVerified, nil
}

func probeSymlink(client *sftp.Client, linkPath, targetPath string) bool {
	if err := client.Symlink(targetPath, linkPath); err != nil {
		return false
	}
	applied, err := client.ReadLink(linkPath)
	return err == nil && applied == targetPath
}

// removeProbeEntries retire celui des deux noms que le serveur a créé ; un nom absent n'est pas une erreur.
func removeProbeEntries(client *sftp.Client, names ...string) error {
	var failures []error
	for _, name := range names {
		if err := client.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
