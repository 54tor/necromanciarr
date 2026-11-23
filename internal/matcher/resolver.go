package matcher

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// resolver tient l'état du départage : alive liste, par fichier du torrent non résolu, les fichiers locaux que le hash n'a pas écartés.
type resolver struct {
	ctx        context.Context
	verifier   torrentcheck.Verifier
	budget     int64
	bytesSpent int64
	torrent    *torrentcheck.Torrent
	dataEnd    int64
	readers    *readerCache
	alive      map[int][]store.MediaFile
	assigned   map[int]store.MediaFile
	ambiguous  map[int][]store.MediaFile
	verdicts   map[verdictKey]bool
}

type verdictKey struct {
	torrentFileIndex int
	localPath        string
	pieceIndex       int
}

// trialPiece est une pièce utilisable pour tester un fichier ; contained signifie qu'elle ne touche aucun autre fichier,
// donc un échec y prouve que le candidat n'est pas le bon.
type trialPiece struct {
	index     int
	contained bool
}

func newResolver(ctx context.Context, options Options, torrent *torrentcheck.Torrent, localBySize map[int64][]store.MediaFile, open OpenFunc) *resolver {
	resolver := &resolver{
		ctx:       ctx,
		verifier:  torrentcheck.Verifier{Limiter: options.Limiter},
		budget:    options.Budget,
		torrent:   torrent,
		readers:   &readerCache{open: open, readers: map[string]io.ReaderAt{}},
		alive:     map[int][]store.MediaFile{},
		assigned:  map[int]store.MediaFile{},
		ambiguous: map[int][]store.MediaFile{},
		verdicts:  map[verdictKey]bool{},
	}
	for torrentFileIndex, file := range torrent.Files {
		resolver.dataEnd = max(resolver.dataEnd, file.Offset+file.Size)
		if candidates := localBySize[file.Size]; file.Size > 0 && len(candidates) > 0 {
			resolver.alive[torrentFileIndex] = slices.Clone(candidates)
		}
	}
	return resolver
}

// run alterne attributions sans lecture et vérifications, jusqu'à ce qu'aucune ne progresse.
func (resolver *resolver) run() error {
	for {
		if resolver.assignUncontested() {
			continue
		}
		progressed, err := resolver.verifyNext()
		if err != nil || !progressed {
			resolver.markAmbiguous(resolver.unresolved()...)
			return err
		}
	}
}

func (resolver *resolver) unresolved() []int {
	return slices.Sorted(maps.Keys(resolver.alive))
}

// assignUncontested affecte sans lecture un candidat unique que nul autre fichier du torrent ne réclame.
func (resolver *resolver) assignUncontested() bool {
	claimCount := map[string]int{}
	for _, candidates := range resolver.alive {
		for _, candidate := range candidates {
			claimCount[candidate.Path]++
		}
	}
	progressed := false
	for _, torrentFileIndex := range resolver.unresolved() {
		candidates := resolver.alive[torrentFileIndex]
		isUncontested := len(candidates) == 1 && claimCount[candidates[0].Path] == 1
		if isUncontested {
			resolver.assign(torrentFileIndex, candidates[0])
			progressed = true
		}
	}
	return progressed
}

func (resolver *resolver) verifyNext() (bool, error) {
	for _, torrentFileIndex := range resolver.unresolved() {
		progressed, err := resolver.narrow(torrentFileIndex)
		if err != nil || progressed {
			return progressed, err
		}
	}
	return false, nil
}

// narrow écarte par hash les fichiers locaux possibles ; progresse si le fichier est résolu ou si un candidat tombe.
func (resolver *resolver) narrow(torrentFileIndex int) (bool, error) {
	candidates := resolver.alive[torrentFileIndex]
	survivors, proven, err := resolver.eliminate(torrentFileIndex, candidates)
	if len(survivors) == 0 {
		delete(resolver.alive, torrentFileIndex)
		return true, err
	}
	resolver.alive[torrentFileIndex] = survivors
	if err != nil {
		return false, err
	}
	if len(survivors) == 1 && proven {
		return true, resolver.claimVerified(torrentFileIndex, survivors[0])
	}
	return len(survivors) < len(candidates), nil
}

// eliminate teste les fichiers locaux pièce par pièce et s'arrête dès qu'un seul candidat a passé une pièce.
// ponytail: des copies identiques sont relues en entier faute de pouvoir les séparer ; Options.Budget borne ce coût.
func (resolver *resolver) eliminate(torrentFileIndex int, candidates []store.MediaFile) (survivors []store.MediaFile, proven bool, err error) {
	survivors = candidates
	for _, piece := range resolver.trialPieces(torrentFileIndex) {
		passers, verifyErr := resolver.passers(torrentFileIndex, piece.index, survivors)
		if verifyErr != nil {
			return survivors, false, verifyErr
		}
		// Sur une pièce partagée, un échec général peut venir du voisin : seul un succès y fait foi.
		isConclusive := len(passers) > 0 || piece.contained
		if isConclusive {
			survivors, proven = passers, len(passers) > 0
		}
		isDecided := len(survivors) == 0 || (len(survivors) == 1 && proven)
		if isDecided {
			break
		}
	}
	return survivors, proven, nil
}

func (resolver *resolver) passers(torrentFileIndex, pieceIndex int, candidates []store.MediaFile) ([]store.MediaFile, error) {
	var passers []store.MediaFile
	for _, candidate := range candidates {
		matches, err := resolver.verify(torrentFileIndex, candidate.Path, pieceIndex)
		if err != nil {
			return nil, err
		}
		if matches {
			passers = append(passers, candidate)
		}
	}
	return passers, nil
}

// claimVerified n'affecte le gagnant que si le hash l'écarte de chaque autre fichier qui le réclame ;
// sinon tous ces fichiers restent ambigus, pour qu'un fichier local ne serve jamais deux fois.
func (resolver *resolver) claimVerified(torrentFileIndex int, winner store.MediaFile) error {
	conflicting := []int{torrentFileIndex}
	for _, rivalIndex := range resolver.unresolved() {
		if rivalIndex == torrentFileIndex || !containsPath(resolver.alive[rivalIndex], winner.Path) {
			continue
		}
		refuted, err := resolver.refutes(rivalIndex, winner.Path)
		if err != nil {
			return err
		}
		if !refuted {
			conflicting = append(conflicting, rivalIndex)
		}
	}
	if len(conflicting) > 1 {
		resolver.markAmbiguous(conflicting...)
		return nil
	}
	resolver.assign(torrentFileIndex, winner)
	return nil
}

func (resolver *resolver) refutes(torrentFileIndex int, localPath string) (bool, error) {
	for _, piece := range resolver.trialPieces(torrentFileIndex) {
		matches, err := resolver.verify(torrentFileIndex, localPath, piece.index)
		if err != nil {
			return false, err
		}
		if !matches && piece.contained {
			resolver.dropCandidate(torrentFileIndex, localPath)
			return true, nil
		}
	}
	return false, nil
}

// dropCandidate retire un fichier local réfuté par le hash, pour qu'aucune passe ultérieure ne l'affecte de nouveau sans lecture.
func (resolver *resolver) dropCandidate(torrentFileIndex int, localPath string) {
	remaining := slices.DeleteFunc(slices.Clone(resolver.alive[torrentFileIndex]), func(candidate store.MediaFile) bool { return candidate.Path == localPath })
	if len(remaining) == 0 {
		delete(resolver.alive, torrentFileIndex)
		return
	}
	resolver.alive[torrentFileIndex] = remaining
}

func (resolver *resolver) assign(torrentFileIndex int, file store.MediaFile) {
	resolver.assigned[torrentFileIndex] = file
	delete(resolver.alive, torrentFileIndex)
	for otherIndex, candidates := range resolver.alive {
		remaining := slices.DeleteFunc(slices.Clone(candidates), func(candidate store.MediaFile) bool { return candidate.Path == file.Path })
		if len(remaining) == 0 {
			delete(resolver.alive, otherIndex)
			continue
		}
		resolver.alive[otherIndex] = remaining
	}
}

func (resolver *resolver) markAmbiguous(torrentFileIndexes ...int) {
	for _, torrentFileIndex := range torrentFileIndexes {
		resolver.ambiguous[torrentFileIndex] = resolver.alive[torrentFileIndex]
		delete(resolver.alive, torrentFileIndex)
	}
}

func containsPath(files []store.MediaFile, path string) bool {
	return slices.ContainsFunc(files, func(file store.MediaFile) bool { return file.Path == path })
}

// verify hache une pièce avec le candidat en place ; le verdict est mis en cache car les voisins attribués ne changent plus.
func (resolver *resolver) verify(torrentFileIndex int, localPath string, pieceIndex int) (bool, error) {
	key := verdictKey{torrentFileIndex: torrentFileIndex, localPath: localPath, pieceIndex: pieceIndex}
	if matches, known := resolver.verdicts[key]; known {
		return matches, nil
	}
	if err := resolver.spend(pieceIndex); err != nil {
		return false, err
	}
	source := &trialSource{cache: resolver.readers, torrentFileIndex: torrentFileIndex, candidatePath: localPath, assigned: resolver.assigned}
	verified, err := resolver.verifier.VerifyPieces(resolver.ctx, resolver.torrent, source, []int{pieceIndex})
	if err == nil {
		err = source.openErr
	}
	if err != nil {
		return false, err
	}
	matches := len(verified) == 1
	resolver.verdicts[key] = matches
	return matches, nil
}

func (resolver *resolver) spend(pieceIndex int) error {
	if err := resolver.ctx.Err(); err != nil {
		return err
	}
	cost := resolver.pieceFileBytes(pieceIndex)
	isOverBudget := resolver.budget > 0 && resolver.bytesSpent+cost > resolver.budget
	if isOverBudget {
		return fmt.Errorf("%w: %d bytes", ErrReadBudgetExceeded, resolver.budget)
	}
	resolver.bytesSpent += cost
	return nil
}

// trialPieces liste les pièces propres au fichier puis celles partagées avec des voisins tous attribués ;
// seules la première et la dernière pièce d'un fichier peuvent être partagées.
func (resolver *resolver) trialPieces(torrentFileIndex int) []trialPiece {
	file := resolver.torrent.Files[torrentFileIndex]
	firstPiece := int(file.Offset / resolver.torrent.PieceLength)
	lastPiece := int((file.Offset + file.Size - 1) / resolver.torrent.PieceLength)
	var contained, shared []trialPiece
	for pieceIndex := firstPiece; pieceIndex <= lastPiece; pieceIndex++ {
		isEdgePiece := pieceIndex == firstPiece || pieceIndex == lastPiece
		neighbours := resolver.neighboursInPiece(pieceIndex, torrentFileIndex, isEdgePiece)
		switch {
		case len(neighbours) == 0:
			contained = append(contained, trialPiece{index: pieceIndex, contained: true})
		case resolver.allAssigned(neighbours):
			shared = append(shared, trialPiece{index: pieceIndex})
		}
	}
	return append(contained, shared...)
}

func (resolver *resolver) neighboursInPiece(pieceIndex, torrentFileIndex int, isEdgePiece bool) []int {
	if !isEdgePiece {
		return nil
	}
	start, end := resolver.pieceBounds(pieceIndex)
	var neighbours []int
	for fileIndex, file := range resolver.torrent.Files {
		if fileIndex != torrentFileIndex && overlapBytes(file, start, end) > 0 {
			neighbours = append(neighbours, fileIndex)
		}
	}
	return neighbours
}

func (resolver *resolver) allAssigned(fileIndexes []int) bool {
	for _, fileIndex := range fileIndexes {
		if _, isAssigned := resolver.assigned[fileIndex]; !isAssigned {
			return false
		}
	}
	return true
}

// pieceFileBytes compte les octets de fichiers d'une pièce, c'est-à-dire ce qui sera lu (le remplissage n'est pas lu).
func (resolver *resolver) pieceFileBytes(pieceIndex int) int64 {
	start, end := resolver.pieceBounds(pieceIndex)
	var total int64
	for _, file := range resolver.torrent.Files {
		total += overlapBytes(file, start, end)
	}
	return total
}

func (resolver *resolver) pieceBounds(pieceIndex int) (start, end int64) {
	start = int64(pieceIndex) * resolver.torrent.PieceLength
	return start, min(start+resolver.torrent.PieceLength, resolver.dataEnd)
}

func overlapBytes(file torrentcheck.File, start, end int64) int64 {
	return max(0, min(end, file.Offset+file.Size)-max(start, file.Offset))
}
