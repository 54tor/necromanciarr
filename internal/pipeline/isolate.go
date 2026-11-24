package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"

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
	ops, err := straddlingLinks(proven, linkRoot)
	if err != nil {
		return IsolationResult{}, fmt.Errorf("%w: %w", ErrPlacementChanged, err)
	}
	prepared, err := prepareCopies(deps, ops)
	if err != nil {
		return IsolationResult{}, err
	}
	if AssignedFilesChanged(assignedFilesOf(proven)) {
		discardCopies(prepared)
		return IsolationResult{}, fmt.Errorf("%w: %s", ErrPlacementChanged, ReasonChangedFile)
	}
	result, err := commitCopies(ctx, deps, ops, prepared)
	if err != nil {
		return result, err
	}
	if _, damage, err := (seedRun{deps: deps}).checkPlacement(ctx, proven, linkRoot); err != nil || damage != "" {
		return result, errors.Join(err, fmt.Errorf("%w: %s", ErrPlacementChanged, damage))
	}
	return result, nil
}

// straddlingLinks liste les liens du dossier qui désignent encore le fichier de la bibliothèque (lien dur ou
// symbolique) et recouvrent une pièce que le client devra télécharger ; un reflink ou une copie est déjà indépendant.
func straddlingLinks(proven provenTorrent, linkRoot string) ([]linker.Op, error) {
	ops, err := linker.Plan(proven.torrent, proven.assignment, linkRoot)
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
		shared, err := sharesLibraryFile(operation)
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
// dans la bibliothèque.
func sharesLibraryFile(operation linker.Op) (bool, error) {
	entryInfo, err := os.Lstat(operation.Destination)
	if err != nil {
		return false, err
	}
	if entryInfo.Mode()&fs.ModeSymlink != 0 {
		return true, nil
	}
	sourceInfo, err := os.Stat(operation.Source)
	if err != nil {
		return false, err
	}
	return os.SameFile(entryInfo, sourceInfo), nil
}

func prepareCopies(deps Deps, ops []linker.Op) ([]linker.PreparedCopy, error) {
	prepare := deps.PrepareCopy
	if prepare == nil {
		prepare = linker.PrepareCopy
	}
	prepared := make([]linker.PreparedCopy, 0, len(ops))
	for _, operation := range ops {
		copied, err := prepare(operation.Source, operation.Destination, operation.Root)
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
func commitCopies(ctx context.Context, deps Deps, ops []linker.Op, prepared []linker.PreparedCopy) (IsolationResult, error) {
	var result IsolationResult
	for copyIndex, copied := range prepared {
		if err := copied.Commit(); err != nil {
			discardCopies(prepared[copyIndex:])
			return result, err
		}
		if err := recordLink(ctx, deps, ops[copyIndex], copied.Kind); err != nil {
			return result, err
		}
		result.Replaced = append(result.Replaced, IsolatedFile{LinkPath: ops[copyIndex].Destination, Kind: copied.Kind.String()})
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
