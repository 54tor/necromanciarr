package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/store"
)

// Erreurs typées d'IsolateStraddlingMedia ; dans les deux cas rien n'a été remplacé.
var (
	// ErrIsolationImpossible : un média à cheval sur une pièce manquante ne peut être ni cloné (reflink) ni copié
	// (copie refusée, place libre insuffisante ou inconnue).
	ErrIsolationImpossible = errors.New("pipeline: a straddling media file cannot be replaced by an independent copy")
	// ErrPlacementChanged : le dossier du torrent ne porte plus les liens et annexes prouvés.
	ErrPlacementChanged = errors.New("pipeline: the torrent link folder no longer matches its proven content")
)

// Raisons d'un refus de Force sur un volume distant, avant toute écriture.
const (
	// ReasonServerCopyUnavailable : le volume du dossier de liens n'offre pas de copie côté serveur prouvée
	// (copy-data) ; une copie par flux ferait transiter tout le média par cet hôte, elle n'est jamais tentée.
	ReasonServerCopyUnavailable = "server_copy_unavailable"
	// ReasonFreeSpaceUnknown : la place libre du dossier de liens ne peut pas être lue (pas de statvfs).
	ReasonFreeSpaceUnknown = "free_space_unknown"
)

// ForceRefusal refuse Force sur un volume distant avant toute écriture ; rien n'a été copié ni changé. Reason est le
// code stable, Volume le volume du dossier de liens, Cause l'erreur typée (fsys.ErrServerCopyUnavailable ou
// linker.ErrFreeSpaceUnknown). La chaîne porte aussi ErrIsolationImpossible.
type ForceRefusal struct {
	Reason, Volume string
	Cause          error
}

func (refusal *ForceRefusal) Error() string {
	return fmt.Sprintf("pipeline: force refused on volume %q (%s): %v", refusal.Volume, refusal.Reason, refusal.Cause)
}

// Unwrap rend ErrIsolationImpossible et la cause.
func (refusal *ForceRefusal) Unwrap() []error { return []error{ErrIsolationImpossible, refusal.Cause} }

// IsolationResult nomme les fichiers du dossier de liens remplacés par une copie indépendante, et la sorte utilisée.
type IsolationResult struct {
	Replaced []IsolatedFile `json:"replaced,omitempty"`
}

// IsolatedFile est un média du dossier de liens désormais copie indépendante (reflink ou copie complète).
type IsolatedFile struct {
	LinkPath string `json:"link_path"`
	Kind     string `json:"kind"`
}

// IsolateStraddlingMedia prépare un partial_annex avant sa reprise : le client écrira chaque pièce manquante en entier,
// donc aussi la part d'un média lié qui la partage. Chaque lien dur ou symbolique vers la bibliothèque qui recouvre
// une pièce manquante est remplacé, sous linkRoot seulement, par une copie indépendante (reflink sinon copie
// complète) ; tout est préparé avant le premier remplacement, et un seul refus laisse tout en place. Le dossier est
// revérifié après coup.
func IsolateStraddlingMedia(ctx context.Context, deps Deps, proposal store.Proposal, candidate store.Candidate, linkRoot string) (IsolationResult, error) {
	proven, reason, err := loadProven(ctx, deps, proposal, candidate)
	if err != nil {
		return IsolationResult{}, err
	}
	if reason != "" {
		return IsolationResult{}, fmt.Errorf("%w: %s", ErrPlacementChanged, reason)
	}
	root, err := locationOfStoredPath(linkRoot)
	if err != nil {
		return IsolationResult{}, fmt.Errorf("%w: %w", ErrPlacementChanged, err)
	}
	ops, err := straddlingLinks(deps, proven, root)
	if err != nil {
		return IsolationResult{}, fmt.Errorf("%w: %w", ErrPlacementChanged, err)
	}
	if len(ops) > 0 {
		if err := checkRemoteCopy(ctx, deps, root); err != nil {
			return IsolationResult{}, err
		}
	}
	prepared, err := prepareCopies(deps, root.Volume, ops)
	if err != nil {
		return IsolationResult{}, err
	}
	changed, err := AssignedFilesChanged(deps.Registry, assignedFilesOf(proven))
	if err != nil || changed {
		discardCopies(prepared)
		return IsolationResult{}, errors.Join(err, fmt.Errorf("%w: %s", ErrPlacementChanged, ReasonChangedFile))
	}
	result, err := commitCopies(ctx, deps, root.Volume, ops, prepared)
	if err != nil {
		return result, err
	}
	if _, damage, err := (seedRun{deps: deps}).checkPlacement(ctx, proven, linkRoot); err != nil || damage != "" {
		return result, errors.Join(err, fmt.Errorf("%w: %s", ErrPlacementChanged, damage))
	}
	return result, nil
}

// checkRemoteCopy refuse, avant toute écriture, une isolation dans un dossier de liens distant sans copie côté
// serveur prouvée (Capabilities.ServerCopy, jamais de repli sur une copie par flux) ou sans place libre connue. Une
// panne du volume reste fsys.ErrRemoteUnavailable, jamais un refus. Le disque local n'est pas concerné.
func checkRemoteCopy(ctx context.Context, deps Deps, root fsys.Location) error {
	if root.Volume == "" {
		return nil
	}
	readFS, _, err := readRegistryOrLocal(deps.Registry).Read(root)
	if err != nil {
		return err
	}
	capabilities, err := readFS.Capabilities(ctx)
	if isRemoteUnavailable(err) {
		return err
	}
	if err != nil || !capabilities.ServerCopy {
		return &ForceRefusal{Reason: ReasonServerCopyUnavailable, Volume: root.Volume, Cause: errors.Join(fsys.ErrServerCopyUnavailable, err)}
	}
	if _, err := readFS.FreeSpace(root.Path); err != nil {
		if isRemoteUnavailable(err) {
			return err
		}
		return &ForceRefusal{Reason: ReasonFreeSpaceUnknown, Volume: root.Volume, Cause: fmt.Errorf("%w: %w", linker.ErrFreeSpaceUnknown, err)}
	}
	return nil
}

// straddlingLinks liste les liens du dossier qui désignent encore le fichier de la bibliothèque (lien dur ou
// symbolique) et recouvrent une pièce que le client devra télécharger ; un reflink ou une copie est déjà indépendant.
func straddlingLinks(deps Deps, proven provenTorrent, root fsys.Location) ([]linker.Op, error) {
	ops, err := planOps(proven, root)
	if err != nil {
		return nil, err
	}
	readFS, _, err := readRegistryOrLocal(deps.Registry).Read(root)
	if err != nil {
		return nil, err
	}
	exposed := filesSharingMissingPieces(proven)
	fileIndexes := slices.Sorted(maps.Keys(proven.assignment.ByTorrentFile))
	var straddling []linker.Op
	for opIndex, operation := range ops {
		if !exposed[fileIndexes[opIndex]] {
			continue
		}
		shared, err := sharesLibraryFile(readFS, operation)
		if err != nil {
			return nil, err
		}
		if shared {
			straddling = append(straddling, operation)
		}
	}
	return straddling, nil
}

// filesSharingMissingPieces marque chaque fichier qui partage une pièce avec un fichier ni lié ni annexe vérifiée :
// le client téléchargera ces pièces entières.
func filesSharingMissingPieces(proven provenTorrent) map[int]bool {
	present := map[int]bool{}
	for fileIndex := range proven.assignment.ByTorrentFile {
		present[fileIndex] = true
	}
	for _, annex := range proven.annexes {
		present[annex.FileIndex] = true
	}
	pieceLength := proven.torrent.PieceLength
	exposed := map[int]bool{}
	for fileIndex, file := range proven.torrent.Files {
		if present[fileIndex] || file.Size <= 0 || pieceLength <= 0 {
			continue
		}
		firstPieceStart := file.Offset / pieceLength * pieceLength
		lastPieceEnd := (file.Offset + file.Size + pieceLength - 1) / pieceLength * pieceLength
		for _, sharingIndex := range proven.torrent.FilesOverlapping(firstPieceStart, lastPieceEnd) {
			exposed[sharingIndex] = true
		}
	}
	return exposed
}

// sharesLibraryFile : un lien symbolique, ou un fichier qui est le même inode que la source, ferait écrire le client
// dans la bibliothèque. À distance, une identité inconnue compte comme partagée : rien ne prouve le contraire.
func sharesLibraryFile(readFS fsys.ReadFS, operation linker.Op) (bool, error) {
	entryInfo, err := readFS.Lstat(operation.Destination)
	if err != nil {
		return false, err
	}
	if entryInfo.Mode()&fs.ModeSymlink != 0 {
		return true, nil
	}
	sourceInfo, err := readFS.Stat(operation.Source)
	if err != nil {
		return false, err
	}
	if readFS.Volume() == "" {
		return local.SameFile(entryInfo, sourceInfo), nil
	}
	entryID, entryKnown := readFS.FileID(entryInfo)
	sourceID, sourceKnown := readFS.FileID(sourceInfo)
	return !entryKnown || !sourceKnown || entryID == sourceID, nil
}

// prepareCopies prépare chaque copie ; les chemins passés à Deps.PrepareCopy sont sous leur forme enregistrée.
func prepareCopies(deps Deps, volume string, ops []linker.Op) ([]linker.PreparedCopy, error) {
	prepare := deps.PrepareCopy
	if prepare == nil {
		prepare = deps.prepareCopyUnder
	}
	prepared := make([]linker.PreparedCopy, 0, len(ops))
	for _, operation := range ops {
		copied, err := prepare(storedIn(volume, operation.Source), storedIn(volume, operation.Destination), storedIn(volume, operation.Root))
		if err != nil {
			discardCopies(prepared)
			return nil, fmt.Errorf("%w: %w", ErrIsolationImpossible, err)
		}
		prepared = append(prepared, copied)
	}
	return prepared, nil
}

func discardCopies(prepared []linker.PreparedCopy) {
	for _, copied := range prepared {
		_ = copied.Discard()
	}
}

// commitCopies met chaque copie en place puis l'enregistre avec sa sorte ; un échec de renommage arrête et retire les
// copies non encore posées.
func commitCopies(ctx context.Context, deps Deps, volume string, ops []linker.Op, prepared []linker.PreparedCopy) (IsolationResult, error) {
	var result IsolationResult
	for copyIndex, copied := range prepared {
		if err := copied.Commit(); err != nil {
			discardCopies(prepared[copyIndex:])
			return result, err
		}
		if err := recordLink(ctx, deps, volume, ops[copyIndex], copied.Kind); err != nil {
			return result, err
		}
		result.Replaced = append(result.Replaced, IsolatedFile{LinkPath: storedIn(volume, ops[copyIndex].Destination), Kind: copied.Kind.String()})
	}
	return result, nil
}

func assignedFilesOf(proven provenTorrent) []store.AssignedFile {
	files := make([]store.AssignedFile, 0, len(proven.assignment.ByTorrentFile))
	for _, media := range proven.assignment.ByTorrentFile {
		files = append(files, store.AssignedFile{Path: media.Path, Size: media.Size, ModTime: media.ModTime})
	}
	return files
}
