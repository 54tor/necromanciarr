package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"strings"
	"sync"

	"necromanciarr/internal/fsys"
	"necromanciarr/internal/fsys/local"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/matcher"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// errReadLocal marque une lecture locale en échec, pour la séparer d'une erreur du store ou du fournisseur.
var errReadLocal = errors.New("pipeline: local read failed")

// LocalFile est un fichier de la bibliothèque ouvert en lecture seule.
type LocalFile interface {
	io.ReaderAt
	io.Closer
}

// openReadOnly ouvre un fichier de la bibliothèque sans jamais pouvoir y écrire : fsys.ReadFS n'offre que la
// lecture. Un chemin sftp://<fournisseur>/… va au volume distant, tout autre chemin au disque local.
func openReadOnly(registry *fsys.Registry, name string) (LocalFile, error) {
	location, err := locationOfStoredPath(name)
	if err != nil {
		return nil, err
	}
	readFS, path, err := registry.Read(location)
	if err != nil {
		return nil, err
	}
	file, err := readFS.Open(path)
	if err != nil {
		return nil, err
	}
	return file, nil
}

// locationOfStoredPath relit un chemin de l'inventaire : brut en local (même un nom à caractère de contrôle, que
// ParseLocation refuserait), Location.String à distance.
func locationOfStoredPath(name string) (fsys.Location, error) {
	if strings.HasPrefix(name, "sftp://") {
		return fsys.ParseLocation(name)
	}
	return fsys.Location{Path: name}, nil
}

// localRegistry est le registre du seul disque local, pris quand Deps.Registry est nil.
var localRegistry = sync.OnceValue(func() *fsys.Registry {
	registry, err := fsys.NewRegistry(map[string]fsys.Volume{"": local.New()}, nil)
	if err != nil {
		panic(err) // un registre à un seul volume local ne peut pas être refusé
	}
	return registry
})

func registryOrLocal(registry *fsys.Registry) *fsys.Registry {
	if registry == nil {
		return localRegistry()
	}
	return registry
}

// openFile est l'ouverture des fichiers de la bibliothèque : OpenLocal (tests) ou le registre.
func (deps Deps) openFile() func(path string) (LocalFile, error) {
	if deps.OpenLocal != nil {
		return deps.OpenLocal
	}
	registry := registryOrLocal(deps.Registry)
	return func(name string) (LocalFile, error) { return openReadOnly(registry, name) }
}

// fileOpener ouvre chaque fichier local une seule fois pour l'association et la vérification, puis les ferme.
type fileOpener struct {
	open  func(path string) (LocalFile, error)
	files map[string]LocalFile
}

func newFileOpener(open func(path string) (LocalFile, error)) *fileOpener {
	return &fileOpener{open: open, files: map[string]LocalFile{}}
}

func (opener *fileOpener) readerAt(filePath string) (io.ReaderAt, error) {
	if file, cached := opener.files[filePath]; cached {
		return file, nil
	}
	file, err := opener.open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open local file: %w", err)
	}
	opener.files[filePath] = file
	return file, nil
}

func (opener *fileOpener) closeAll() {
	for _, file := range opener.files {
		_ = file.Close()
	}
}

// compositeSource présente au vérificateur les fichiers locaux associés et les annexes vérifiées, gardées en mémoire.
type compositeSource struct {
	readers map[int]io.ReaderAt
}

// ReaderAt donne le fichier du torrent d'index fileIndex, absent s'il n'est ni local ni annexe vérifiée.
func (source compositeSource) ReaderAt(fileIndex int) (io.ReaderAt, bool) {
	reader, present := source.readers[fileIndex]
	return reader, present
}

func (source compositeSource) with(fileIndex int, data []byte) compositeSource {
	readers := maps.Clone(source.readers)
	readers[fileIndex] = bytes.NewReader(data)
	return compositeSource{readers: readers}
}

func buildSource(assignment matcher.Assignment, annexes map[int][]byte, opener *fileOpener) (compositeSource, error) {
	source := compositeSource{readers: map[int]io.ReaderAt{}}
	for fileIndex, mediaFile := range assignment.ByTorrentFile {
		reader, err := opener.readerAt(mediaFile.Path)
		if err != nil {
			return compositeSource{}, err
		}
		source.readers[fileIndex] = reader
	}
	for fileIndex, data := range annexes {
		source.readers[fileIndex] = bytes.NewReader(data)
	}
	return source, nil
}

// storedAnnexes reprend les annexes déjà vérifiées du candidat ; leur index est celui de torrentcheck.Torrent.Files.
func (run candidateRun) storedAnnexes(ctx context.Context, candidate store.Candidate, torrent *torrentcheck.Torrent) (map[int][]byte, error) {
	stored, err := run.deps.Store.ListVerifiedFiles(ctx, candidate.ID)
	if err != nil {
		return nil, err
	}
	annexes := map[int][]byte{}
	for _, file := range stored {
		isSameFile := file.FileIndex < len(torrent.Files) && torrent.Files[file.FileIndex].Path == file.Path &&
			torrent.Files[file.FileIndex].Size == int64(len(file.Data))
		if isSameFile {
			annexes[file.FileIndex] = file.Data
		}
	}
	return annexes, nil
}

// missingNfos liste les .nfo du torrent absents de la source, non vides et conservables (au plus 1 Mio).
func missingNfos(torrent *torrentcheck.Torrent, assignment matcher.Assignment, source compositeSource) []int {
	var missing []int
	for fileIndex, file := range torrent.Files {
		_, present := source.ReaderAt(fileIndex)
		_, ambiguous := assignment.Ambiguous[fileIndex]
		isNfo := strings.EqualFold(path.Ext(file.Path), ".nfo")
		isStorable := file.Size > 0 && file.Size <= store.MaxVerifiedFileBytes
		if isNfo && isStorable && !present && !ambiguous {
			missing = append(missing, fileIndex)
		}
	}
	return missing
}

// completeNfos tente de compléter chaque .nfo absent avec ceux du fournisseur ; une variante vérifiée est conservée
// et ajoutée à la source renvoyée, un .nfo sans variante vérifiée reste manquant.
func (run candidateRun) completeNfos(ctx context.Context, candidate store.Candidate, torrent *torrentcheck.Torrent,
	assignment matcher.Assignment, source compositeSource,
) (compositeSource, error) {
	missing := missingNfos(torrent, assignment, source)
	if len(missing) == 0 {
		return source, nil
	}
	nfos, err := run.deps.Sources.Nfos(ctx, candidate.Infohash, candidate.Tracker)
	if err != nil {
		return source, nfoFetchError(run.result, err)
	}
	for _, fileIndex := range missing {
		data, found, err := run.findVerifyingVariant(ctx, torrent, fileIndex, nfos, source)
		if err != nil {
			return source, err
		}
		if !found {
			continue
		}
		verified := store.VerifiedFile{CandidateID: candidate.ID, FileIndex: fileIndex, Path: torrent.Files[fileIndex].Path, Data: data}
		if err := run.deps.Store.SaveVerifiedFile(ctx, verified); err != nil {
			return source, err
		}
		source = source.with(fileIndex, data)
	}
	return source, nil
}

// nfoFetchError arrête l'étape si le fournisseur est limité, indisponible ou refuse l'accès ; un .nfo inconnu ou
// illisible laisse simplement l'annexe manquante.
func nfoFetchError(result *Result, err error) error {
	if stopErr := stopError(StageVerify, err); stopErr != nil {
		return stopErr
	}
	if !errors.Is(err, libraryprovider.ErrNotFound) {
		result.count(ReasonNfoFetchFailed, 1)
	}
	return nil
}

// findVerifyingVariant essaie chaque .nfo du fournisseur sous chacune de ses versions de même taille que le fichier
// du torrent ; une variante n'est retenue que si toutes les pièces contrôlables qui la recouvrent concordent.
func (run candidateRun) findVerifyingVariant(ctx context.Context, torrent *torrentcheck.Torrent, fileIndex int,
	nfos []libraryprovider.Nfo, source compositeSource,
) ([]byte, bool, error) {
	testable, everyPieceTestable := testablePieces(torrent, fileIndex, source)
	if !everyPieceTestable {
		// Une pièce à cheval sur un autre fichier absent ne prouverait rien : l'annexe reste manquante.
		return nil, false, nil
	}
	verifier := torrentcheck.Verifier{Limiter: run.deps.ReadLimiter}
	tried := map[string]bool{}
	for _, nfo := range nfos {
		for _, variant := range torrentcheck.NfoVariants(nfo.Data) {
			isCandidate := int64(len(variant.Data)) == torrent.Files[fileIndex].Size && !tried[string(variant.Data)]
			if !isCandidate {
				continue
			}
			tried[string(variant.Data)] = true
			verified, err := verifier.VerifyPieces(ctx, torrent, source.with(fileIndex, variant.Data), testable)
			if err != nil && ctx.Err() == nil {
				return nil, false, fmt.Errorf("%w: %w", errReadLocal, err)
			}
			if err != nil {
				return nil, false, err
			}
			if len(verified) == len(testable) {
				return variant.Data, true, nil
			}
		}
	}
	return nil, false, nil
}

// testablePieces liste les pièces qui recouvrent le fichier ; everyPieceTestable est vrai seulement si chaque autre
// fichier de chacune de ces pièces est disponible.
func testablePieces(torrent *torrentcheck.Torrent, fileIndex int, source compositeSource) (pieces []int, everyPieceTestable bool) {
	file := torrent.Files[fileIndex]
	firstPiece := int(file.Offset / torrent.PieceLength)
	lastPiece := int((file.Offset + file.Size - 1) / torrent.PieceLength)
	for pieceIndex := firstPiece; pieceIndex <= lastPiece; pieceIndex++ {
		if !neighboursPresent(torrent, pieceIndex, fileIndex, source) {
			return nil, false
		}
		pieces = append(pieces, pieceIndex)
	}
	return pieces, true
}

func neighboursPresent(torrent *torrentcheck.Torrent, pieceIndex, fileIndex int, source compositeSource) bool {
	start := int64(pieceIndex) * torrent.PieceLength
	for _, otherIndex := range torrent.FilesOverlapping(start, start+torrent.PieceLength) {
		if otherIndex == fileIndex {
			continue
		}
		if _, present := source.ReaderAt(otherIndex); !present {
			return false
		}
	}
	return true
}
