package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/linker"
	"necromanciarr/internal/qbit"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// uncoveredTracker n'a ni entrée trackers ni .torrent scellé : ses propositions sont no_announce.
const uncoveredTracker = "tracker-uncovered"

// Annonce d'un .torrent d'indexeur : passkey et hôte recherchés partout où le torrent ne doit jamais aller.
const (
	indexerTracker  = "Tracker Indexer"
	indexerHost     = "indexer-tracker.example.com"
	indexerPasskey  = "fake-passkey-indexer"
	indexerAnnounce = "https://" + indexerHost + "/announce?passkey=" + indexerPasskey
)

func testSealer(t *testing.T) *sources.Sealer {
	t.Helper()
	sealer, err := sources.NewSealer(bytes.Repeat([]byte{1}, sources.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

// bindingOf rebâtit la liaison du scellement depuis la ligne stockée du candidat, comme Seed.
func bindingOf(candidate store.Candidate) sources.Binding {
	return sources.Binding{Infohash: candidate.Infohash, Tracker: candidate.Tracker, Source: sources.ID(candidate.Source)}
}

// sealIndexerTorrent scelle plain sous binding et le range sur le candidat, comme le ferait Verify pour un indexeur.
func (fixture *applyFixture) sealIndexerTorrent(candidateID int64, plain []byte, binding sources.Binding) {
	fixture.t.Helper()
	if fixture.sealer == nil {
		fixture.sealer = testSealer(fixture.t)
	}
	blob, err := fixture.sealer.Seal(plain, binding)
	if err != nil {
		fixture.t.Fatal(err)
	}
	if err := fixture.store.SaveCandidateSealed(context.Background(), candidateID, blob, fixedNow); err != nil {
		fixture.t.Fatal(err)
	}
}

// indexerTorrentOf rend le .torrent complet qu'un indexeur aurait servi pour ce candidat : même info, son annonce.
func (fixture *applyFixture) indexerTorrentOf(candidate store.Candidate) []byte {
	fixture.t.Helper()
	plain, err := sources.InjectAnnounce(fixture.fetcher.torrents[candidate.Infohash], [][]string{{indexerAnnounce}})
	if err != nil {
		fixture.t.Fatal(err)
	}
	return plain
}

// linkedOn vérifie, accepte et lie un candidat de ce tracker ; beforeLink s'exécute après Verify (scellement).
func (fixture *applyFixture) linkedOn(tracker string, entries []torrentEntry, beforeLink func(candidate store.Candidate)) (store.Candidate, store.Proposal) {
	fixture.t.Helper()
	candidate := fixture.addCandidate("Movie.2020", tracker, entries)
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	if beforeLink != nil {
		beforeLink(fixture.candidate(candidate.Infohash))
	}
	fixture.accept(proposal.ID)
	if result := fixture.mustRunStage(StageLink); result.Linked != 1 {
		fixture.t.Fatalf("link: gotValue %+v, expectedValue one torrent linked", result)
	}
	return fixture.candidate(candidate.Infohash), proposal
}

func announcesOf(t *testing.T, torrent []byte) [][]string {
	t.Helper()
	tiers, err := sources.AnnounceTiers(torrent)
	if err != nil {
		t.Fatalf("announce of the added torrent: %v", err)
	}
	return [][]string(tiers)
}

func isPrivate(t *testing.T, torrent []byte) bool {
	t.Helper()
	metaInfo, err := metainfo.Load(bytes.NewReader(torrent))
	if err != nil {
		t.Fatal(err)
	}
	info, err := metaInfo.UnmarshalInfo()
	if err != nil {
		t.Fatal(err)
	}
	return info.Private != nil && *info.Private
}

func privateEntriesTorrent(t *testing.T, name string, entries []torrentEntry) []byte {
	t.Helper()
	info := testTorrentInfo(name, entries)
	private := true
	info.Private = &private
	return encodeTestTorrent(t, info)
}

func TestVerify_marksProposalNoAnnounce(t *testing.T) {
	fixture := newVerifyFixture(t)
	uncovered := fixture.addCandidate("Movie.A", uncoveredTracker, movieOnly())
	covered := fixture.addCandidate("Other.B", "tracker-b", []torrentEntry{{path: "other.mkv", content: patternBytes(7, 48), localName: "other.mkv"}})

	result := fixture.mustRun()

	if gotValue := result.Reasons[ReasonNoAnnounce]; gotValue != 1 {
		t.Fatalf("gotValue %d no_announce in %+v, expectedValue the uncovered proposal counted once", gotValue, result.Reasons)
	}
	resolver := fixture.deps().Announces
	expectedSources := map[int64]sources.AnnounceSource{uncovered.ID: sources.AnnounceNone, covered.ID: sources.AnnounceConfig}
	for candidateID, expectedValue := range expectedSources {
		stored := fixture.proposalOf(candidateID, store.ProposalFull)
		var candidate store.Candidate
		for _, listed := range candidatesByHash(t, fixture.store) {
			if listed.ID == candidateID {
				candidate = listed
			}
		}
		details := decodeDetails(t, stored).WithAnnounce(AnnounceSourceOf(resolver, candidate))
		if details.AnnounceSource != string(expectedValue) || details.Seedable == nil || *details.Seedable != (expectedValue != sources.AnnounceNone) {
			t.Fatalf("candidate %d: gotValue %+v, expectedValue source %s", candidateID, details, expectedValue)
		}
		if bytes.Contains(stored.Details, []byte("seedable")) || bytes.Contains(stored.Details, []byte("announce_source")) {
			t.Fatalf("gotValue stored details %s, expectedValue seedable and announce_source never stored", stored.Details)
		}
	}
}

func TestLink_refusesNoAnnounceProposal(t *testing.T) {
	fixture := newApplyFixture(t)
	probed := 0
	fixture.probe = func(directory string) (linker.Capabilities, error) {
		probed++
		return linker.Probe(directory)
	}
	candidate := fixture.addCandidate("Movie.2020", uncoveredTracker, movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	before := linkDirEntries(t, fixture.linkDir)

	result := fixture.mustRunStage(StageLink)

	// La sonde écrit des fichiers d'essai dans le dossier de liens : elle vient avant toute création, donc le refus aussi.
	if after := linkDirEntries(t, fixture.linkDir); !slices.Equal(after, before) || probed != 0 {
		t.Fatalf("gotValue entries %v (before %v) and %d probes, expectedValue nothing probed nor created", after, before, probed)
	}
	isRefused := fixture.decisionOf(proposal.ID) == store.ProposalFailed && result.Reasons[ReasonNoAnnounce] == 1 &&
		result.Linked == 0 && fixture.progressOf(proposal.ID).Step == ""
	if !isRefused {
		t.Fatalf("gotValue %+v decision %s, expectedValue a no_announce refusal", result, fixture.decisionOf(proposal.ID))
	}
	links, err := fixture.store.ListLinks(context.Background(), "")
	if err != nil || len(links) != 0 {
		t.Fatalf("gotValue links %v %v, expectedValue none recorded", links, err)
	}
}

// probedLinkDir compte les sondes du dossier de liens : la sonde y écrit des fichiers d'essai, donc un refus qui doit
// précéder toute création doit aussi la précéder.
func probedLinkDir(fixture *applyFixture) *int {
	probed := 0
	fixture.probe = func(directory string) (linker.Capabilities, error) {
		probed++
		return linker.Probe(directory)
	}
	return &probed
}

func TestLink_refusesSealedCandidateWithoutKeyBeforeCreatingAnything(t *testing.T) {
	fixture := newApplyFixture(t)
	probed := probedLinkDir(fixture)
	candidate := fixture.addCandidate("Movie.2020", indexerTracker, movieOnly())
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	verified := fixture.candidate(candidate.Infohash)
	fixture.sealIndexerTorrent(verified.ID, fixture.indexerTorrentOf(verified), bindingOf(verified))
	fixture.accept(proposal.ID)
	fixture.sealer = nil
	before := linkDirEntries(t, fixture.linkDir)

	result := fixture.mustRunStage(StageLink)

	if after := linkDirEntries(t, fixture.linkDir); !slices.Equal(after, before) || *probed != 0 {
		t.Fatalf("gotValue entries %v and %d probes, expectedValue nothing probed nor created", after, *probed)
	}
	if result.Reasons[ReasonSecretKeyMissing] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed {
		t.Fatalf("gotValue %+v, expectedValue a secret_key_missing refusal at Link", result)
	}
}

func TestLinkAndSeed_refuseAProviderCandidateWithAnEmptyTracker(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.keepEmptyTracker = true
	probed := probedLinkDir(fixture)
	candidate := fixture.addCandidate("Movie.2020", "", movieOnly())
	if stored := fixture.candidate(candidate.Infohash); stored.Tracker != "" || stored.HasSealed {
		t.Fatalf("gotValue %+v, expectedValue a provider candidate with an empty tracker and no sealed torrent", stored)
	}
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)

	linkResult := fixture.mustRunStage(StageLink)
	// Réacceptée comme le ferait l'opérateur, puis marquée liée : seule la défense de Seed reste.
	if err := fixture.store.AcceptProposals(context.Background(), []int64{proposal.ID}, fixedNow); err != nil {
		t.Fatal(err)
	}
	progress := store.ProposalProgress{ProposalID: proposal.ID, Step: store.StepLinked, LinkRoot: fixture.linkDir}
	if err := fixture.store.SaveProposalProgress(context.Background(), progress, fixedNow); err != nil {
		t.Fatal(err)
	}
	seedResult := fixture.mustRunStage(StageSeed)

	isRefused := linkResult.Reasons[ReasonNoAnnounce] == 1 && seedResult.Reasons[ReasonNoAnnounce] == 1 && *probed == 0 &&
		len(fixture.client.received) == 0 && len(linkDirEntries(t, fixture.linkDir)) == 0
	if !isRefused {
		t.Fatalf("gotValue link %+v seed %+v probes %d, expectedValue no_announce at Link and Seed, nothing created or added",
			linkResult, seedResult, *probed)
	}
}

func TestSeed_refusesNoAnnounceProposal(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	fixture.trackers = nil

	result := fixture.mustRunStage(StageSeed)

	isRefused := len(fixture.client.added) == 0 && len(fixture.client.received) == 0 &&
		fixture.decisionOf(proposal.ID) == store.ProposalFailed && result.Reasons[ReasonNoAnnounce] == 1
	if !isRefused || slices.Contains(fixture.client.paused, candidate.Infohash) {
		t.Fatalf("gotValue %+v added %v, expectedValue a no_announce refusal before anything reaches the client", result, fixture.client.added)
	}
}

func TestSeed_refusesNoAnnounceEvenForATorrentAlreadyInTheClient(t *testing.T) {
	fixture := newApplyFixture(t)
	candidate, proposal := fixture.linkedProposal(movieOnly(), store.ProposalFull)
	linkRoot := fixture.progressOf(proposal.ID).LinkRoot
	fixture.client.existing = []qbit.TorrentInfo{{Hash: candidate.Infohash, SavePath: linkRoot, Progress: 1, State: "stoppedUP"}}
	fixture.trackers = nil

	result := fixture.mustRunStage(StageSeed)

	isUntouched := len(fixture.client.paused) == 0 && len(fixture.client.resumed) == 0 && len(fixture.client.guardCalls) == 0
	if !isUntouched || result.Reasons[ReasonNoAnnounce] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed {
		t.Fatalf("gotValue %+v paused %v resumed %v, expectedValue a no_announce refusal before taking the torrent over",
			result, fixture.client.paused, fixture.client.resumed)
	}
}

func TestSeed_publicTorrentWithoutAnnounceIsRefused(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	raw := buildTestTorrent(t, "Movie.2020", movieOnly())
	if isPrivate(t, raw) {
		t.Fatal("expectedValue a public test torrent")
	}
	candidate := fixture.addCandidateWithTorrent("Movie.2020", uncoveredTracker, movieOnly(), raw)
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	// Lié avant la mise à jour : seule la défense de Seed reste.
	progress := store.ProposalProgress{ProposalID: proposal.ID, Step: store.StepLinked, LinkRoot: fixture.linkDir}
	if err := fixture.store.SaveProposalProgress(context.Background(), progress, fixedNow); err != nil {
		t.Fatal(err)
	}

	result := fixture.mustRunStage(StageSeed)

	if len(fixture.client.received) != 0 || result.Reasons[ReasonNoAnnounce] != 1 || fixture.decisionOf(proposal.ID) != store.ProposalFailed {
		t.Fatalf("gotValue %+v, expectedValue a public torrent without announce refused like a private one (no DHT-only seed)", result)
	}
}

func TestSeed_injectsConfiguredAnnounceInMemory(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	raw := privateEntriesTorrent(t, "Movie.2020", movieOnly())
	candidate := fixture.addCandidateWithTorrent("Movie.2020", "TRACKER-A", movieOnly(), raw)
	proposal := fixture.verifiedProposal(candidate, store.ProposalFull)
	fixture.accept(proposal.ID)
	fixture.mustRunStage(StageLink)

	fixture.mustRunStage(StageSeed)

	added, present := fixture.client.added[candidate.Infohash]
	if !present || torrentInfohash(added.torrent) != candidate.Infohash || !isPrivate(t, added.torrent) {
		t.Fatalf("gotValue %v, expectedValue the candidate's private torrent added with its infohash", present)
	}
	if gotValue := announcesOf(t, added.torrent); !slices.EqualFunc(gotValue, [][]string{{trackerAAnnounce}}, slices.Equal) {
		t.Fatalf("gotValue %v, expectedValue the tracker-a configured announce (matched through NormalizeTracker)", gotValue)
	}
	stored, err := fixture.store.CandidateTorrent(context.Background(), candidate.ID)
	if err != nil || bytes.Contains(stored, []byte("announce")) || bytes.Contains(stored, []byte("fake-passkey")) {
		t.Fatalf("gotValue stored copy with announce or passkey (%v), expectedValue the sanitized copy untouched", err)
	}
	if fixture.decisionOf(proposal.ID) != store.ProposalApplied || fixture.progressOf(proposal.ID).Step != store.StepSeeded {
		t.Fatalf("gotValue step %s, expectedValue seeded", fixture.progressOf(proposal.ID).Step)
	}
}

func TestSeed_injectsTheIndexerAnnounceWhenNoTrackerEntryCoversIt(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	candidate, proposal := fixture.linkedOn(indexerTracker, movieOnly(), func(verified store.Candidate) {
		fixture.sealIndexerTorrent(verified.ID, fixture.indexerTorrentOf(verified), bindingOf(verified))
	})

	fixture.mustRunStage(StageSeed)

	added := fixture.client.added[candidate.Infohash]
	if gotValue := announcesOf(t, added.torrent); !slices.EqualFunc(gotValue, [][]string{{indexerAnnounce}}, slices.Equal) {
		t.Fatalf("gotValue %v, expectedValue the sealed indexer announce", gotValue)
	}
	if fixture.progressOf(proposal.ID).Step != store.StepSeeded {
		t.Fatalf("gotValue step %s, expectedValue seeded", fixture.progressOf(proposal.ID).Step)
	}
}

func TestSeed_unsealsAndChecksInfohashBeforeAdd(t *testing.T) {
	cases := map[string]struct {
		seal           func(fixture *applyFixture, verified store.Candidate)
		afterLink      func(fixture *applyFixture, linked store.Candidate)
		withoutSealer  bool
		expectedReason string
	}{
		"sealed for another infohash": {
			seal: func(fixture *applyFixture, verified store.Candidate) {
				binding := bindingOf(verified)
				binding.Infohash = strings.Repeat("0", 40)
				fixture.sealIndexerTorrent(verified.ID, fixture.indexerTorrentOf(verified), binding)
			},
			expectedReason: ReasonSealedTorrentUnreadable,
		},
		"sealed under another key": {
			seal: func(fixture *applyFixture, verified store.Candidate) {
				other, err := sources.NewSealer(bytes.Repeat([]byte{2}, sources.KeySize))
				if err != nil {
					fixture.t.Fatal(err)
				}
				fixture.sealer = other
				fixture.sealIndexerTorrent(verified.ID, fixture.indexerTorrentOf(verified), bindingOf(verified))
				fixture.sealer = testSealer(fixture.t)
			},
			expectedReason: ReasonSealedTorrentUnreadable,
		},
		"sealed plaintext of another torrent": {
			seal: func(fixture *applyFixture, verified store.Candidate) {
				other := buildTestTorrent(fixture.t, "Other.2021", []torrentEntry{{path: "other.mkv", content: patternBytes(9, 48)}})
				plain, err := sources.InjectAnnounce(other, [][]string{{indexerAnnounce}})
				if err != nil {
					fixture.t.Fatal(err)
				}
				fixture.sealIndexerTorrent(verified.ID, plain, bindingOf(verified))
			},
			expectedReason: ReasonInfohashChangedAfterInject,
		},
		"stored copy replaced by another infohash after link": {
			afterLink: func(fixture *applyFixture, linked store.Candidate) {
				info := testTorrentInfo("Movie.2020", movieOnly())
				info.Source = "other-source"
				infoBytes, err := bencode.Marshal(info)
				if err != nil {
					fixture.t.Fatal(err)
				}
				replacement, err := bencode.Marshal(metainfo.MetaInfo{InfoBytes: infoBytes})
				if err != nil {
					fixture.t.Fatal(err)
				}
				if err := fixture.store.SaveCandidateTorrent(context.Background(), linked.ID, replacement, fixedNow); err != nil {
					fixture.t.Fatal(err)
				}
			},
			expectedReason: ReasonInfohashChangedAfterInject,
		},
		"sealed without a key in this run": {
			seal: func(fixture *applyFixture, verified store.Candidate) {
				fixture.sealIndexerTorrent(verified.ID, fixture.indexerTorrentOf(verified), bindingOf(verified))
			},
			withoutSealer:  true,
			expectedReason: ReasonSecretKeyMissing,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			fixture.skipRecheck = true
			tracker := indexerTracker
			if testCase.seal == nil {
				tracker = testTracker
			}
			var beforeLink func(store.Candidate)
			if testCase.seal != nil {
				beforeLink = func(verified store.Candidate) { testCase.seal(fixture, verified) }
			}
			candidate, proposal := fixture.linkedOn(tracker, movieOnly(), beforeLink)
			if testCase.afterLink != nil {
				testCase.afterLink(fixture, candidate)
			}
			if testCase.withoutSealer {
				fixture.sealer = nil
			}

			result := fixture.mustRunStage(StageSeed)

			isRefused := len(fixture.client.received) == 0 && len(fixture.client.added) == 0 &&
				result.Reasons[testCase.expectedReason] == 1 && fixture.decisionOf(proposal.ID) == store.ProposalFailed
			if !isRefused {
				t.Fatalf("gotValue %+v received %d, expectedValue %s and nothing added", result, len(fixture.client.received), testCase.expectedReason)
			}
		})
	}
}

// writeRecorder garde le texte de chaque écriture du store pendant Seed ; failApply fait échouer ApplyProposal avec un
// texte qui cite la passkey, comme une couche basse qui la recopierait.
type writeRecorder struct {
	Store
	mutex     sync.Mutex
	writes    []string
	failApply error
}

func (recorder *writeRecorder) record(values ...any) {
	recorder.mutex.Lock()
	defer recorder.mutex.Unlock()
	for _, value := range values {
		if raw, isBytes := value.([]byte); isBytes {
			recorder.writes = append(recorder.writes, string(raw))
			continue
		}
		recorder.writes = append(recorder.writes, fmt.Sprintf("%+v", value))
	}
}

func (recorder *writeRecorder) SaveCandidateTorrent(ctx context.Context, candidateID int64, raw []byte, now time.Time) error {
	recorder.record(raw)
	return recorder.Store.SaveCandidateTorrent(ctx, candidateID, raw, now)
}

func (recorder *writeRecorder) SaveProposalProgress(ctx context.Context, progress store.ProposalProgress, now time.Time) error {
	recorder.record(progress)
	return recorder.Store.SaveProposalProgress(ctx, progress, now)
}

func (recorder *writeRecorder) ApplyProposal(ctx context.Context, proposalID int64, step store.ProposalStep, linkRoot string, now time.Time) error {
	recorder.record(step, linkRoot)
	if recorder.failApply != nil {
		return recorder.failApply
	}
	return recorder.Store.ApplyProposal(ctx, proposalID, step, linkRoot, now)
}

func (recorder *writeRecorder) AddProposalWarning(ctx context.Context, proposalID int64, warning i18n.Message) error {
	recorder.record(warning)
	return recorder.Store.AddProposalWarning(ctx, proposalID, warning)
}

func (recorder *writeRecorder) SetProposalDecision(ctx context.Context, proposalID int64, decision store.ProposalDecision, now time.Time) error {
	recorder.record(decision)
	return recorder.Store.SetProposalDecision(ctx, proposalID, decision, now)
}

func (recorder *writeRecorder) ClearProposalStep(ctx context.Context, proposalID int64, now time.Time) error {
	recorder.record(proposalID)
	return recorder.Store.ClearProposalStep(ctx, proposalID, now)
}

func (recorder *writeRecorder) SetCandidateState(ctx context.Context, candidateID int64, state store.CandidateState, now time.Time) error {
	recorder.record(state)
	return recorder.Store.SetCandidateState(ctx, candidateID, state, now)
}

func (recorder *writeRecorder) UpsertProposal(ctx context.Context, proposal store.Proposal, now time.Time) (int64, bool, error) {
	recorder.record(proposal, proposal.Details)
	return recorder.Store.UpsertProposal(ctx, proposal, now)
}

// captureLogs détourne les journaux standard (log et slog) pendant le test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previousSlog, previousFlags := slog.Default(), log.Flags()
	previousOutput := log.Writer()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&buffer)
	t.Cleanup(func() {
		slog.SetDefault(previousSlog)
		log.SetOutput(previousOutput)
		log.SetFlags(previousFlags)
	})
	return &buffer
}

// leakedForms liste les écritures de la passkey et de l'hôte qui ne doivent apparaître nulle part.
var leakedForms = []string{indexerPasskey, indexerHost, testAnnounce, "fake-passkey-test", "tracker-test.example.com"}

func assertNoAnnounceIn(t *testing.T, where, text string) {
	t.Helper()
	for _, form := range leakedForms {
		if strings.Contains(text, form) {
			t.Fatalf("%s: gotValue %q inside, expectedValue no announce host nor passkey", where, form)
		}
	}
}

func TestSeed_injectedTorrentNeverStoredOrLogged(t *testing.T) {
	cases := map[string]struct {
		tracker   string
		clientErr func(torrent []byte) error
		applyErr  bool
	}{
		"indexer announce added":       {tracker: indexerTracker},
		"config announce added":        {tracker: testTracker},
		"client echoes the torrent":    {tracker: indexerTracker, clientErr: func(torrent []byte) error { return fmt.Errorf("HTTP 500: %s", torrent) }},
		"read-only client echoes it":   {tracker: testTracker, clientErr: func(torrent []byte) error { return fmt.Errorf("%w: %s", qbit.ErrReadOnly, torrent) }},
		"stopping error quotes the pk": {tracker: indexerTracker, applyErr: true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newApplyFixture(t)
			fixture.skipRecheck = true
			var beforeLink func(store.Candidate)
			if testCase.tracker == indexerTracker {
				beforeLink = func(verified store.Candidate) {
					fixture.sealIndexerTorrent(verified.ID, fixture.indexerTorrentOf(verified), bindingOf(verified))
				}
			}
			candidate, _ := fixture.linkedOn(testCase.tracker, movieOnly(), beforeLink)
			recorder := &writeRecorder{Store: fixture.store}
			fixture.stageStore = recorder
			fixture.client.addErrorFor = testCase.clientErr
			errLowerLayer := errors.New("low layer failed near " + indexerAnnounce)
			if testCase.applyErr {
				recorder.failApply = errLowerLayer
			}
			logs := captureLogs(t)

			result, err := fixture.runStage(StageSeed)

			if len(fixture.client.received) != 1 {
				t.Fatalf("gotValue %d adds, expectedValue the torrent handed to Add once", len(fixture.client.received))
			}
			if gotValue := fixture.client.received[0]; len(bytes.Trim(gotValue, "\x00")) != 0 {
				t.Fatal("gotValue the injected buffer still readable after Add, expectedValue it cleared")
			}
			encoded, marshalErr := json.Marshal(result)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			assertNoAnnounceIn(t, "step result", string(encoded))
			assertNoAnnounceIn(t, "store writes", strings.Join(recorder.writes, "\n"))
			assertNoAnnounceIn(t, "logs", logs.String())
			// Une erreur qui arrête l'étape garde l'hôte (schéma://hôte/***), jamais la passkey découverte.
			if err != nil && strings.Contains(err.Error(), indexerPasskey) {
				t.Fatalf("stage error: gotValue %q, expectedValue the discovered passkey masked", err)
			}
			if testCase.applyErr && !errors.Is(err, errLowerLayer) {
				t.Fatalf("gotValue %v, expectedValue the stopping error kept for errors.Is", err)
			}
			for _, warning := range fixture.proposalsOf(candidate.ID)[0].Warnings {
				assertNoAnnounceIn(t, "warnings", fmt.Sprint(warning))
			}
		})
	}
}

func TestProposalDetails_announceFieldsAreNeverStored(t *testing.T) {
	encoded, err := encodeStoredDetails(ProposalDetails{Percent: 100}.WithAnnounce(sources.AnnounceConfig))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	_, hasSeedable := fields["seedable"]
	_, hasSource := fields["announce_source"]
	if hasSeedable || hasSource || string(fields["percent"]) != "100" {
		t.Fatalf("gotValue %s, expectedValue the stored details without the computed fields", encoded)
	}
}

func TestProposalDetails_announceFieldsAreAbsentUntilComputed(t *testing.T) {
	encoded, err := json.Marshal(ProposalDetails{Percent: 100})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("seedable")) || bytes.Contains(encoded, []byte("announce_source")) {
		t.Fatalf("gotValue %s, expectedValue no seedable nor announce_source for a kind without torrent", encoded)
	}
	computed, err := json.Marshal(ProposalDetails{}.WithAnnounce(sources.AnnounceNone))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(computed, []byte(`"seedable":false`)) || !bytes.Contains(computed, []byte(`"announce_source":"none"`)) {
		t.Fatalf("gotValue %s, expectedValue seedable false kept once computed", computed)
	}
}

func TestNativeCrossSeed_eachMemberResolvesOwnAnnounce(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	fixture.crossSeed = crossseed.Native
	first := fixture.addCandidate("Movie.A", "tracker-a", movieOnly())
	second := fixture.addCandidate("Movie.B", "tracker-b", movieOnly())
	fixture.mustRun()
	primary := fixture.proposalOf(first.ID, store.ProposalFull)
	member := fixture.proposalOf(second.ID, store.ProposalCrossSeed)
	fixture.accept(primary.ID)
	fixture.accept(member.ID)
	fixture.mustRunStage(StageLink)

	fixture.mustRunStage(StageSeed)

	expectedAnnounces := map[string]string{first.Infohash: trackerAAnnounce, second.Infohash: trackerBAnnounce}
	for infohash, expectedValue := range expectedAnnounces {
		added, present := fixture.client.added[infohash]
		if !present {
			t.Fatalf("gotValue %s not added, expectedValue both members seeded", infohash)
		}
		if gotValue := announcesOf(t, added.torrent); !slices.EqualFunc(gotValue, [][]string{{expectedValue}}, slices.Equal) {
			t.Fatalf("member %s: gotValue %v, expectedValue its own tracker announce %s", infohash, gotValue, expectedValue)
		}
	}
}

func TestNativeCrossSeed_memberWithoutAnnounceIsRefusedAlone(t *testing.T) {
	fixture := newApplyFixture(t)
	fixture.skipRecheck = true
	fixture.crossSeed = crossseed.Native
	first := fixture.addCandidate("Movie.A", "tracker-a", movieOnly())
	second := fixture.addCandidate("Movie.B", uncoveredTracker, movieOnly())
	fixture.mustRun()
	primary := fixture.proposalOf(first.ID, store.ProposalFull)
	member := fixture.proposalOf(second.ID, store.ProposalCrossSeed)
	fixture.accept(primary.ID)
	fixture.accept(member.ID)

	linkResult := fixture.mustRunStage(StageLink)
	fixture.mustRunStage(StageSeed)

	_, primaryAdded := fixture.client.added[first.Infohash]
	_, memberAdded := fixture.client.added[second.Infohash]
	if !primaryAdded || memberAdded || linkResult.Reasons[ReasonNoAnnounce] != 1 || fixture.decisionOf(member.ID) != store.ProposalFailed {
		t.Fatalf("gotValue primary %v member %v link %+v, expectedValue only the member without announce refused",
			primaryAdded, memberAdded, linkResult)
	}
}
