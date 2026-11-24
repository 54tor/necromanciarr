package doctor

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/linker"
)

const (
	hintSameVolume = "put the library and the link directory on the same mounted volume (one bind mount covering both)"
	hintVFSLinks   = "on an rclone mount, restart rclone with --vfs-links to allow symlinks"
	hintModTime    = "on an rclone VFS mount, a modification time round trip may come from the write cache and not from the remote"
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
		return Result{Status: Skipped, Detail: "no link directory or torrent root configured"}
	}
	kinds := allowedKinds(settings)
	var collected findings
	for _, directory := range directories {
		if _, resolveErr := filepath.EvalSymlinks(directory); resolveErr != nil {
			collected.add(Fail, directory+": le dossier ne peut pas être résolu, aucune sonde lancée", "créer le dossier ou corriger son chemin")
			continue
		}
		if root, inside := insideLibraryRoot(directory, settings.LibraryRoots); inside {
			collected.add(Fail, fmt.Sprintf("%s: le dossier de liens est dans une racine de bibliothèque (%s), aucune sonde lancée", directory, root),
				"choisir un dossier de liens hors de la bibliothèque : elle n'est jamais écrite")
			continue
		}
		collected.add(probeDirectory(directory, kinds, deps))
		for _, root := range settings.LibraryRoots {
			collected.add(compareVolumes(root, directory, kinds, deps))
		}
	}
	return collected.result()
}

// probeDirectory crée puis supprime les entrées d'essai de linker.Probe dans directory, et nulle part ailleurs.
func probeDirectory(directory string, kinds []linker.Kind, deps Deps) (Status, string, string) {
	capabilities, err := deps.Probe(directory)
	if err != nil {
		return Fail, fmt.Sprintf("%s: cannot probe links: %v", directory, err), "check that the directory exists and is writable"
	}
	accepted := map[linker.Kind]bool{linker.Hardlink: capabilities.Hardlink, linker.Symlink: capabilities.Symlink, linker.Reflink: capabilities.Reflink}
	lines := describeKinds(accepted, capabilities)
	var hints []string
	detail := fmt.Sprintf("%s: %s", directory, strings.Join(lines, ", "))
	status := statusForKinds(accepted, kinds)
	if !accepted[linker.Symlink] && isFuse(directory, deps) {
		hints = append(hints, hintVFSLinks)
	}
	return status, detail, strings.Join(hints, separatorDetail)
}

func describeKinds(accepted map[linker.Kind]bool, capabilities linker.Capabilities) (lines []string) {
	for _, kind := range []linker.Kind{linker.Hardlink, linker.Symlink, linker.Reflink} {
		if accepted[kind] {
			lines = append(lines, kind.String()+" ok")
			continue
		}
		lines = append(lines, fmt.Sprintf("%s refused (%v)", kind, capabilities.Reasons[kind]))
	}
	return lines
}

// statusForKinds : Fail si aucune sorte voulue n'est acceptée, Warn si la préférée l'est moins que les suivantes.
func statusForKinds(accepted map[linker.Kind]bool, wanted []linker.Kind) Status {
	if len(wanted) == 0 {
		return OK
	}
	if !slices.ContainsFunc(wanted, func(kind linker.Kind) bool { return accepted[kind] }) {
		return Fail
	}
	if !accepted[wanted[0]] {
		return Warn
	}
	return OK
}

func isFuse(directory string, deps Deps) bool {
	kind, err := deps.FilesystemKind(directory)
	return err == nil && kind == linker.Fuse
}

// compareVolumes ne touche pas au disque : types de volume et périphériques seulement, jamais de création dans la racine.
func compareVolumes(libraryRoot, linkDirectory string, kinds []linker.Kind, deps Deps) (Status, string, string) {
	label := fmt.Sprintf("%s -> %s", libraryRoot, linkDirectory)
	same, err := deps.SameVolume(libraryRoot, linkDirectory)
	if err != nil {
		return Warn, label + ": volumes cannot be compared: " + err.Error(), "check that both paths exist"
	}
	if same {
		return OK, label + ": same volume", ""
	}
	detail := fmt.Sprintf("%s: different volumes (%s and %s)", label, volumeName(libraryRoot, deps), volumeName(linkDirectory, deps))
	if slices.Contains(kinds, linker.Symlink) {
		return Warn, detail + ", only symlinks can cross", hintSameVolume
	}
	return Fail, detail + ", hardlinks and reflinks need one volume", hintSameVolume
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
		return Result{Status: Skipped, Detail: "no filesystem provider configured"}
	}
	reports, err := deps.DescribeMounts(ctx, settings.FSProviders)
	if err != nil {
		return Result{Status: Fail, Detail: "cannot build the providers: " + err.Error(), Hint: "check fsProviders in config.json"}
	}
	var collected findings
	for _, report := range reports {
		status := OK
		if len(report.Advice) > 0 {
			status = Warn
		}
		collected.add(status, describeMount(report), strings.Join(report.Advice, separatorDetail))
	}
	collected.hints = append(collected.hints, hintModTime)
	return collected.result()
}

func describeMount(report hashsource.ProviderMountReport) string {
	hashes := make([]string, 0, len(report.Capabilities.Hashes))
	for _, algorithm := range report.Capabilities.Hashes {
		hashes = append(hashes, string(algorithm))
	}
	return fmt.Sprintf("%s: %s -> %s (mode %s, hashes [%s], modtime %t)", report.Provider,
		filepath.Clean(report.Mount.LocalPath), report.Mount.Remote, report.Mode, strings.Join(hashes, " "), report.Capabilities.ModTime)
}
