package local

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"necromanciarr/internal/fsys"
)

const (
	linkProbePrefix     = ".necromanciarr-link-probe-"
	linkProbeRandomSize = 16
	linkProbeContent    = "x"
	reasonUnsupported   = "unsupported"
	reasonCrossDevice   = "cross_device"
	reasonLinkRefused   = "link_refused"
	reasonProbeFailed   = "probe_failed"
)

// ProbeLinks mesure, dans root, les liens dur, symbolique et reflink par de vrais essais sur un fichier temporaire,
// puis retire tout ce qu'il a créé ; une sorte refusée porte sa raison dans Reasons.
func ProbeLinks(root fsys.WriteRoot) (fsys.Capabilities, error) {
	sourceName, err := probeName()
	if err != nil {
		return fsys.Capabilities{}, err
	}
	if err := writeProbeSource(root, sourceName); err != nil {
		return fsys.Capabilities{}, err
	}
	defer func() { _ = root.Remove(sourceName) }()
	source := fsys.Location{Path: filepath.Join(root.Root().Path, sourceName)}
	capabilities := fsys.Capabilities{Reasons: map[string]string{}}
	capabilities.Hardlink = tryLink(root, capabilities.Reasons, "hardlink", sourceName+"-h", func(name string) error {
		return root.Hardlink(source, name)
	})
	capabilities.Symlink = tryLink(root, capabilities.Reasons, "symlink", sourceName+"-s", func(name string) error {
		return root.Symlink(sourceName, name)
	})
	capabilities.Reflink = tryLink(root, capabilities.Reasons, "reflink", sourceName+"-r", func(name string) error {
		return reflinkInto(root, source, name)
	})
	return capabilities, nil
}

func probeName() (string, error) {
	randomBytes := make([]byte, linkProbeRandomSize)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("local: probe name: %w", err)
	}
	return linkProbePrefix + hex.EncodeToString(randomBytes), nil
}

func writeProbeSource(root fsys.WriteRoot, name string) error {
	file, err := root.CreateExclusive(name, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write([]byte(linkProbeContent))
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return errors.Join(err, root.Remove(name))
	}
	return nil
}

// tryLink essaie une sorte de lien vers name puis retire name ; un échec note sa raison sous function.
func tryLink(root fsys.WriteRoot, reasons map[string]string, function, name string, link func(string) error) bool {
	err := link(name)
	_ = root.Remove(name)
	if err == nil {
		return true
	}
	reasons[function] = linkReason(err)
	return false
}

func reflinkInto(root fsys.WriteRoot, source fsys.Location, name string) error {
	reflinker, canReflink := root.(fsys.Reflinker)
	if !canReflink {
		return ErrUnsupported
	}
	return reflinker.Reflink(source, name)
}

func linkReason(err error) string {
	switch {
	case errors.Is(err, ErrUnsupported):
		return reasonUnsupported
	case errors.Is(err, ErrCrossDevice):
		return reasonCrossDevice
	case errors.Is(err, ErrLinkRefused):
		return reasonLinkRefused
	}
	return reasonProbeFailed
}
