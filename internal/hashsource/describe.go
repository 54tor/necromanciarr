package hashsource

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"path/filepath"
	"time"

	"necromanciarr/internal/i18n"
)

// maxSampleSearchEntries borne le parcours qui cherche un fichier d'essai sur un montage réseau.
const maxSampleSearchEntries = 2000

// Conseils affichés par DescribeMount : des messages à rendre, jamais du texte.
var (
	adviceStartRC     = i18n.Message{Key: i18n.KeyAdviceMountStartRC}
	adviceNoHash      = i18n.Message{Key: i18n.KeyAdviceMountNoHash}
	adviceNoModTime   = i18n.Message{Key: i18n.KeyAdviceMountNoModTime}
	adviceModTimeless = i18n.Message{Key: i18n.KeyAdviceMountModTimeUnknown}
	adviceVFSLinks    = i18n.Message{Key: i18n.KeyAdviceMountVFSLinks}
	adviceRcloneError = i18n.Message{Key: i18n.KeyAdviceMountRcloneError}
	adviceNoSample    = i18n.Message{Key: i18n.KeyAdviceMountNoSample}
)

// DescribeOptions complète DescribeMountWith. SamplePath vide : un fichier est cherché sous le montage. WritableDir
// vide : les symlinks ne sont pas sondés.
type DescribeOptions struct {
	RCURL       string
	Binary      string
	Timeout     time.Duration
	SamplePath  string
	WritableDir string
}

// MountReport résume ce qu'on sait d'un montage, pour le contrôle `rclone` de doctor.
type MountReport struct {
	Mount         Mount
	Mode          string
	BinaryFound   bool
	Capabilities  Capabilities
	SymlinksKnown bool
	Probed        bool
	Advice        []i18n.Message
}

// Modes de MountReport.Mode : interrogation par l'API rc, par le binaire, ou aucune des deux.
const (
	ModeRC     = "rc"
	ModeBinary = "binary"
	ModeNone   = "none"
)

// DescribeMount sonde le montage avec le binaire rclone du PATH, sans API rc ni sonde de symlink.
func DescribeMount(ctx context.Context, mount Mount) MountReport {
	return DescribeMountWith(ctx, mount, DescribeOptions{})
}

// DescribeMountWith demande une empreinte d'essai pour chaque algorithme, lit la date du fichier d'essai et explique
// en Advice ce qui manque. Rien n'est écrit, hors les entrées d'essai de linker.ProbeDirectory dans WritableDir.
func DescribeMountWith(ctx context.Context, mount Mount, options DescribeOptions) MountReport {
	report := MountReport{Mount: mount, Mode: ModeNone}
	source, err := NewRclone(RcloneOptions{RCURL: options.RCURL, Binary: options.Binary, Mounts: []Mount{mount}, Timeout: options.Timeout})
	if err != nil {
		report.Advice = append(report.Advice, i18n.Message{Key: i18n.KeyAdviceMountInvalid, Params: map[string]string{"error": err.Error()}})
		return report
	}
	report.Mode, report.BinaryFound = detectMode(options)
	if report.Mode == ModeNone {
		report.Advice = append(report.Advice, adviceStartRC)
		return report
	}
	samplePath := options.SamplePath
	if samplePath == "" {
		samplePath = findSampleFile(ctx, mount.LocalPath)
	}
	capabilities, err := ProbeCapabilities(ctx, source, samplePath, options.WritableDir)
	report.Advice = append(report.Advice, adviceFor(err, capabilities, options.WritableDir != "")...)
	if err != nil {
		return report
	}
	report.Probed, report.Capabilities, report.SymlinksKnown = true, capabilities, options.WritableDir != ""
	return report
}

func detectMode(options DescribeOptions) (mode string, binaryFound bool) {
	if options.RCURL != "" {
		return ModeRC, false
	}
	binary := options.Binary
	if binary == "" {
		binary = defaultRcloneBinary
	}
	if _, err := exec.LookPath(binary); err != nil {
		return ModeNone, false
	}
	return ModeBinary, true
}

func adviceFor(probeErr error, capabilities Capabilities, symlinksProbed bool) []i18n.Message {
	var advice []i18n.Message
	switch {
	case errors.Is(probeErr, ErrNoSampleFile):
		return []i18n.Message{adviceNoSample}
	case probeErr != nil:
		return []i18n.Message{adviceRcloneError}
	}
	if len(capabilities.Hashes) == 0 {
		advice = append(advice, adviceNoHash, adviceStartRC)
	}
	if !capabilities.ModTime && !symlinksProbed {
		advice = append(advice, adviceModTimeless)
	}
	if !capabilities.ModTime && symlinksProbed {
		advice = append(advice, adviceNoModTime)
	}
	if symlinksProbed && !capabilities.Symlinks {
		advice = append(advice, adviceVFSLinks)
	}
	return advice
}

// findSampleFile renvoie le premier fichier régulier non vide trouvé, ou "" ; le parcours est borné.
func findSampleFile(ctx context.Context, root string) string {
	var sample string
	visited := 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		visited++
		if err != nil || ctx.Err() != nil || visited > maxSampleSearchEntries {
			return fs.SkipAll
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil && info.Size() > 0 {
			sample = path
			return fs.SkipAll
		}
		return nil
	})
	return sample
}
