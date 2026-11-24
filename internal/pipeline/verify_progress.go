package pipeline

import (
	"context"
	"time"

	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
)

// Fréquence par défaut d'enregistrement de l'avancement : assez souvent pour ne perdre que peu de lecture sur une
// coupure, assez peu pour ne pas écrire en base à chaque pièce.
const (
	defaultProgressEvery    = 64
	defaultProgressInterval = 5 * time.Second
)

// cover vérifie toutes les pièces en sautant celles que la vérification enregistrée a déjà validées ; complete est faux
// si la couverture n'a pas abouti (lecture en échec : l'avancement est enregistré et le candidat sera repris).
func (run candidateRun) cover(ctx context.Context, candidate store.Candidate, torrent *torrentcheck.Torrent,
	source compositeSource,
) (torrentcheck.Report, bool, error) {
	tracker, err := run.newProgressTracker(ctx, candidate.ID, len(torrent.Pieces))
	if err != nil {
		return torrentcheck.Report{}, false, err
	}
	if tracker.resumed {
		run.result.Resumed++
	}
	verifier := torrentcheck.Verifier{Limiter: run.deps.ReadLimiter, Progress: tracker.record}
	report, err := verifier.Coverage(ctx, torrent, source, tracker.alreadyVerified())
	if err == nil {
		return report, true, nil
	}
	if lockLost(ctx) {
		// Un autre processus tient le verrou : plus aucune écriture.
		return report, false, err
	}
	// Un enregistrement périodique coupé par l'annulation n'empêche pas l'enregistrement final.
	if tracker.saveErr != nil && ctx.Err() == nil {
		return report, false, tracker.saveErr
	}
	if saveErr := tracker.save(context.WithoutCancel(ctx)); saveErr != nil {
		return report, false, saveErr
	}
	if ctx.Err() != nil {
		return report, false, err
	}
	run.countReadError()
	return report, false, nil
}

// progressTracker tient les pièces contrôlées et vérifiées pendant la couverture et les enregistre régulièrement.
type progressTracker struct {
	ctx           context.Context
	deps          Deps
	candidateID   int64
	totalPieces   int
	checked       []byte
	verified      []byte
	verifiedCount int
	resumed       bool
	sinceSave     int
	lastSave      time.Time
	saveErr       error
}

// newProgressTracker repart de la vérification enregistrée si elle porte sur le même nombre de pièces ; une
// vérification invalidée (fichier changé) a déjà été effacée par le store.
func (run candidateRun) newProgressTracker(ctx context.Context, candidateID int64, totalPieces int) (*progressTracker, error) {
	tracker := &progressTracker{
		ctx: ctx, deps: run.deps, candidateID: candidateID, totalPieces: totalPieces,
		checked: newBitset(totalPieces), verified: newBitset(totalPieces), lastSave: time.Now(),
	}
	previous, found, err := run.deps.Store.LoadVerification(ctx, candidateID)
	if err != nil {
		return nil, err
	}
	isUsable := found && previous.TotalPieces == totalPieces && len(previous.VerifiedBitset) == len(tracker.verified) &&
		len(previous.CheckedBitset) == len(tracker.checked)
	if !isUsable {
		return tracker, nil
	}
	copy(tracker.checked, previous.CheckedBitset)
	copy(tracker.verified, previous.VerifiedBitset)
	tracker.verifiedCount = countBits(tracker.verified, totalPieces)
	tracker.resumed = true
	return tracker, nil
}

func (tracker *progressTracker) alreadyVerified() map[int]struct{} {
	verified := map[int]struct{}{}
	for pieceIndex := range tracker.totalPieces {
		if hasBit(tracker.verified, pieceIndex) {
			verified[pieceIndex] = struct{}{}
		}
	}
	return verified
}

// record note une pièce hachée et enregistre l'avancement quand il est dû ; un verrou perdu arrête la couverture.
func (tracker *progressTracker) record(pieceIndex int, verified bool) error {
	if lockLost(tracker.ctx) {
		return ErrLockLost
	}
	setBit(tracker.checked, pieceIndex)
	if verified && !hasBit(tracker.verified, pieceIndex) {
		setBit(tracker.verified, pieceIndex)
		tracker.verifiedCount++
	}
	tracker.sinceSave++
	isDue := tracker.sinceSave >= tracker.every() || time.Since(tracker.lastSave) >= tracker.interval()
	if !isDue {
		return nil
	}
	if err := tracker.save(tracker.ctx); err != nil {
		tracker.saveErr = err
		return err
	}
	return nil
}

func (tracker *progressTracker) every() int {
	if tracker.deps.ProgressEvery <= 0 {
		return defaultProgressEvery
	}
	return tracker.deps.ProgressEvery
}

func (tracker *progressTracker) interval() time.Duration {
	if tracker.deps.ProgressInterval <= 0 {
		return defaultProgressInterval
	}
	return tracker.deps.ProgressInterval
}

// save enregistre une vérification en cours, reprenable.
func (tracker *progressTracker) save(ctx context.Context) error {
	tracker.sinceSave, tracker.lastSave = 0, time.Now()
	return tracker.deps.Store.SaveVerification(ctx, store.Verification{
		CandidateID: tracker.candidateID, TotalPieces: tracker.totalPieces, VerifiedPieces: tracker.verifiedCount,
		CheckedBitset: tracker.checked, VerifiedBitset: tracker.verified,
		Percent: percentOf(tracker.verifiedCount, tracker.totalPieces), State: store.VerificationInProgress,
	}, tracker.deps.now())
}

// completedVerification traduit le rapport final : toutes les pièces sont contrôlées, seules celles dont le SHA1
// concorde sont vérifiées.
func completedVerification(candidateID int64, report torrentcheck.Report) store.Verification {
	checked, verified := newBitset(report.TotalPieces), newBitset(report.TotalPieces)
	for pieceIndex := range report.TotalPieces {
		setBit(checked, pieceIndex)
	}
	for pieceIndex := range report.VerifiedSet() {
		setBit(verified, pieceIndex)
	}
	return store.Verification{
		CandidateID: candidateID, TotalPieces: report.TotalPieces, VerifiedPieces: report.VerifiedPieces,
		CheckedBitset: checked, VerifiedBitset: verified, MediaComplete: report.MediaComplete,
		Percent: percentOf(report.VerifiedPieces, report.TotalPieces), State: store.VerificationComplete,
	}
}

func percentOf(verifiedPieces, totalPieces int) float64 {
	if totalPieces == 0 {
		return 0
	}
	return float64(verifiedPieces) * 100 / float64(totalPieces)
}

// Bitset du store : bit i = octet i/8, bit de poids faible i%8.
func newBitset(bitCount int) []byte { return make([]byte, (bitCount+7)/8) }

func setBit(bitset []byte, bitIndex int) { bitset[bitIndex/8] |= 1 << (bitIndex % 8) }

func hasBit(bitset []byte, bitIndex int) bool { return bitset[bitIndex/8]&(1<<(bitIndex%8)) != 0 }

func countBits(bitset []byte, bitCount int) int {
	count := 0
	for bitIndex := range bitCount {
		if hasBit(bitset, bitIndex) {
			count++
		}
	}
	return count
}
