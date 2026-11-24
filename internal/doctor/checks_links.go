package doctor

import (
	"context"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
)

const (
	kindAccepted = "✓"
	kindRefused  = "✗"
)

// linkKindByName relie les noms de dedupe.kinds et linkType aux sortes de liens sondées.
var linkKindByName = map[string]linker.Kind{"hardlink": linker.Hardlink, "symlink": linker.Symlink, "reflink": linker.Reflink}

// linkDirectories rassemble les dossiers où des liens sont créés : linkDirs et racines torrent de dedupe.
func linkDirectories(settings config.Config) []string {
	var directories []string
	for _, candidate := range append(slices.Clone(settings.LinkDirs), settings.Dedupe.TorrentRoots...) {
		if !slices.Contains(directories, candidate) {
			directories = append(directories, candidate)
		}
	}
	return directories
}

func allowedKinds(settings config.Config) []linker.Kind {
	names := settings.Dedupe.Kinds
	if len(names) == 0 {
		names = []string{settings.LinkType}
	}
	var kinds []linker.Kind
	for _, name := range names {
		if kind, known := linkKindByName[name]; known {
			kinds = append(kinds, kind)
		}
	}
	return kinds
}

func checkLinkFeasibility(settings config.Config, deps Deps) Result {
	directories := linkDirectories(settings)
	if len(directories) == 0 {
		return Result{Status: Skipped, Code: CodeNoLinkDirectory, Detail: message(i18n.KeyDoctorLinksNone)}
	}
	kinds := allowedKinds(settings)
	var collected findings
	for _, directory := range directories {
		if _, resolveErr := filepath.EvalSymlinks(directory); resolveErr != nil {
			collected.add(Fail, CodeLinkDirUnresolved, message(i18n.KeyDoctorLinkDirUnresolved, "dir", directory),
				message(i18n.KeyDoctorLinkDirUnresolvedHint))
			continue
		}
		if root, inside := insideLibraryRoot(directory, settings.LibraryRoots); inside {
			collected.add(Fail, CodeLinkDirInLibrary, message(i18n.KeyDoctorLinkDirInLibrary, "dir", directory, "root", root),
				message(i18n.KeyDoctorLinkDirInLibraryHint))
			continue
		}
		status, code, detail, hint := probeDirectory(directory, kinds, deps)
		collected.add(status, code, detail, hint)
		for _, root := range settings.LibraryRoots {
			status, code, detail, hint = compareVolumes(root, directory, kinds, deps)
			collected.add(status, code, detail, hint)
		}
	}
	return collected.result()
}

// probeDirectory crée puis supprime les entrées d'essai de linker.Probe dans directory, et nulle part ailleurs.
func probeDirectory(directory string, kinds []linker.Kind, deps Deps) (Status, string, i18n.Message, i18n.Message) {
	capabilities, err := deps.Probe(directory)
	if err != nil {
		return Fail, CodeLinkProbeFailed, message(i18n.KeyDoctorLinkProbeFailed, "dir", directory, "error", err.Error()),
			message(i18n.KeyDoctorLinkProbeFailedHint)
	}
	accepted := map[linker.Kind]bool{linker.Hardlink: capabilities.Hardlink, linker.Symlink: capabilities.Symlink, linker.Reflink: capabilities.Reflink}
	detail := message(i18n.KeyDoctorLinkKinds, "dir", directory,
		"hardlink", describeKind(linker.Hardlink, accepted, capabilities), "symlink", describeKind(linker.Symlink, accepted, capabilities),
		"reflink", describeKind(linker.Reflink, accepted, capabilities))
	var hint i18n.Message
	if !accepted[linker.Symlink] && isFuse(directory, deps) {
		hint = message(i18n.KeyDoctorLinkVFSHint)
	}
	status, code := statusForKinds(accepted, kinds)
	return status, code, detail, hint
}

// describeKind résume une sorte de lien par des signes, identiques dans toutes les langues ; le motif du refus est
// le texte technique de la sonde.
func describeKind(kind linker.Kind, accepted map[linker.Kind]bool, capabilities linker.Capabilities) string {
	if accepted[kind] {
		return kindAccepted
	}
	if reason := capabilities.Reasons[kind]; reason != nil {
		return kindRefused + " (" + reason.Error() + ")"
	}
	return kindRefused
}

// statusForKinds : Fail si aucune sorte voulue n'est acceptée, Warn si la préférée l'est moins que les suivantes.
func statusForKinds(accepted map[linker.Kind]bool, wanted []linker.Kind) (Status, string) {
	if len(wanted) == 0 {
		return OK, CodeLinkKindsOK
	}
	if !slices.ContainsFunc(wanted, func(kind linker.Kind) bool { return accepted[kind] }) {
		return Fail, CodeLinkKindsRefused
	}
	if !accepted[wanted[0]] {
		return Warn, CodeLinkKindFallback
	}
	return OK, CodeLinkKindsOK
}

func isFuse(directory string, deps Deps) bool {
	kind, err := deps.FilesystemKind(directory)
	return err == nil && kind == linker.Fuse
}

// compareVolumes ne touche pas au disque : types de volume et périphériques seulement, jamais de création dans la racine.
func compareVolumes(libraryRoot, linkDirectory string, kinds []linker.Kind, deps Deps) (Status, string, i18n.Message, i18n.Message) {
	same, err := deps.SameVolume(libraryRoot, linkDirectory)
	if err != nil {
		return Warn, CodeVolumesNotComparable,
			message(i18n.KeyDoctorVolumesNotComparable, "from", libraryRoot, "to", linkDirectory, "error", err.Error()),
			message(i18n.KeyDoctorVolumesNotComparableHint)
	}
	if same {
		return OK, CodeSameVolume, message(i18n.KeyDoctorVolumesSame, "from", libraryRoot, "to", linkDirectory), i18n.Message{}
	}
	params := []string{"from", libraryRoot, "to", linkDirectory, "from_kind", volumeName(libraryRoot, deps), "to_kind", volumeName(linkDirectory, deps)}
	if slices.Contains(kinds, linker.Symlink) {
		return Warn, CodeVolumesDifferSymlink, message(i18n.KeyDoctorVolumesDifferSymlink, params...), message(i18n.KeyDoctorSameVolumeHint)
	}
	return Fail, CodeVolumesDiffer, message(i18n.KeyDoctorVolumesDiffer, params...), message(i18n.KeyDoctorSameVolumeHint)
}

func volumeName(path string, deps Deps) string {
	kind, err := deps.FilesystemKind(path)
	if err != nil {
		return "unknown"
	}
	return kind.String()
}

func describeMounts(ctx context.Context, providers []config.FSProvider) ([]hashsource.ProviderMountReport, error) {
	registry, err := hashsource.NewFromConfig(providers, hashsource.Deps{})
	if err != nil {
		return nil, err
	}
	return registry.DescribeMounts(ctx), nil
}

func checkFSProviders(ctx context.Context, settings config.Config, deps Deps) Result {
	if len(settings.FSProviders) == 0 {
		return Result{Status: Skipped, Code: CodeNoFSProvider, Detail: message(i18n.KeyDoctorFSNone)}
	}
	reports, err := deps.DescribeMounts(ctx, settings.FSProviders)
	if err != nil {
		return Result{
			Status: Fail, Code: CodeFSBuildFailed,
			Detail: message(i18n.KeyDoctorFSBuildFailed, "error", err.Error()), Hint: message(i18n.KeyDoctorFSBuildFailedHint),
		}
	}
	var collected findings
	for _, report := range reports {
		if len(report.Advice) == 0 {
			collected.add(OK, CodeMountOK, describeMount(report), i18n.Message{})
			continue
		}
		collected.add(Warn, CodeMountAdvice, describeMount(report), report.Advice[0])
		collected.hints = append(collected.hints, report.Advice[1:]...)
	}
	collected.hints = append(collected.hints, message(i18n.KeyDoctorFSModTimeHint))
	return collected.result()
}

func describeMount(report hashsource.ProviderMountReport) i18n.Message {
	hashes := make([]string, 0, len(report.Capabilities.Hashes))
	for _, algorithm := range report.Capabilities.Hashes {
		hashes = append(hashes, string(algorithm))
	}
	return message(i18n.KeyDoctorFSMount, "provider", report.Provider, "local", filepath.Clean(report.Mount.LocalPath),
		"remote", report.Mount.Remote, "mode", report.Mode, "hashes", strings.Join(hashes, " "),
		"modtime", strconv.FormatBool(report.Capabilities.ModTime))
}
