package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

var fixedNow = time.Unix(1700000000, 0).UTC()

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	openedStore, err := store.Open(filepath.Join(t.TempDir(), "necromanciarr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedStore.Close() })
	return openedStore
}

// fakeResumer enregistre les torrents démarrés par Force.
type fakeResumer struct{ resumed []string }

func (resumer *fakeResumer) Resume(_ context.Context, infoHash string) error {
	resumer.resumed = append(resumer.resumed, infoHash)
	return nil
}

// decisionFixture construit des candidats vérifiés et leurs propositions directement par le store.
type decisionFixture struct {
	t       *testing.T
	store   *store.Store
	resumer *fakeResumer
	media   store.MediaFile
}

func newDecisionFixture(t *testing.T) *decisionFixture {
	t.Helper()
	openedStore := openTestStore(t)
	ctx := context.Background()
	if err := openedStore.UpsertMediaFile(ctx, store.MediaFile{Path: "/home/u/media/movie.mkv", Size: 40, ModTime: fixedNow, TMDBID: 603}); err != nil {
		t.Fatal(err)
	}
	mediaFiles, err := openedStore.ListMediaFiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &decisionFixture{t: t, store: openedStore, resumer: &fakeResumer{}, media: mediaFiles[0]}
}

func (fixture *decisionFixture) service(behavior crossseed.Behavior) Service {
	return New(Deps{Store: fixture.store, Client: fixture.resumer, CrossSeed: behavior, Now: func() time.Time { return fixedNow }})
}

// verifiedCandidate enregistre un candidat associé au fichier local partagé, puis le passe à verified et le regroupe.
func (fixture *decisionFixture) verifiedCandidate(infohash string) store.Candidate {
	fixture.t.Helper()
	ctx := context.Background()
	file := store.CandidateFile{FileIndex: 0, Path: "Movie/movie.mkv", Size: 40, MediaFileID: fixture.media.ID, MediaSize: 40, MediaModTime: fixedNow}
	candidateID, _, err := fixture.store.SaveCandidate(ctx, store.Candidate{Infohash: infohash, Name: "Movie " + infohash, TotalSize: 40}, []store.CandidateFile{file}, fixedNow)
	if err != nil {
		fixture.t.Fatal(err)
	}
	for _, state := range []store.CandidateState{store.CandidateFetched, store.CandidateVerified} {
		if err := fixture.store.SetCandidateState(ctx, candidateID, state, fixedNow); err != nil {
			fixture.t.Fatal(err)
		}
	}
	if err := fixture.store.RegroupCandidates(ctx); err != nil {
		fixture.t.Fatal(err)
	}
	return fixture.candidate(candidateID)
}

func (fixture *decisionFixture) candidate(candidateID int64) store.Candidate {
	fixture.t.Helper()
	candidates, err := fixture.store.ListCandidatesByState(context.Background())
	if err != nil {
		fixture.t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.ID == candidateID {
			return candidate
		}
	}
	fixture.t.Fatalf("candidate %d not found", candidateID)
	return store.Candidate{}
}

func (fixture *decisionFixture) proposal(candidate store.Candidate, kind store.ProposalKind) int64 {
	fixture.t.Helper()
	proposalID, _, err := fixture.store.UpsertProposal(context.Background(), store.Proposal{
		CandidateID: candidate.ID, GroupID: pipeline.GroupOf(fixture.candidate(candidate.ID)), Kind: kind,
		Warnings: []string{"warning of " + string(kind)},
	}, fixedNow)
	if err != nil {
		fixture.t.Fatal(err)
	}
	return proposalID
}

func (fixture *decisionFixture) stored(proposalID int64) store.Proposal {
	fixture.t.Helper()
	proposals, err := fixture.store.ListProposals(context.Background(), "")
	if err != nil {
		fixture.t.Fatal(err)
	}
	for _, proposal := range proposals {
		if proposal.ID == proposalID {
			return proposal
		}
	}
	fixture.t.Fatalf("proposal %d not found", proposalID)
	return store.Proposal{}
}

func (fixture *decisionFixture) seedPaused(proposalID int64) {
	fixture.t.Helper()
	ctx := context.Background()
	if err := fixture.store.SetProposalDecision(ctx, proposalID, store.ProposalAccepted, fixedNow); err != nil {
		fixture.t.Fatal(err)
	}
	if err := fixture.store.ApplyProposal(ctx, proposalID, store.StepSeededPausedIncomplete, "/home/u/links/Movie [aa]", fixedNow); err != nil {
		fixture.t.Fatal(err)
	}
}

func TestApp_acceptRefusesConflictAndPartialPackWithoutForce(t *testing.T) {
	fixture := newDecisionFixture(t)
	service := fixture.service(crossseed.Delegate)
	ctx := context.Background()
	conflictID := fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalConflict)
	packID := fixture.proposal(fixture.verifiedCandidate("bb"), store.ProposalPartialPack)

	if err := service.Accept(ctx, conflictID); !errors.Is(err, ErrConflictNeedsChoose) {
		t.Fatalf("conflict: gotValue %v, expectedValue ErrConflictNeedsChoose", err)
	}
	if err := service.Accept(ctx, packID); !errors.Is(err, ErrPartialPackNeedsForce) {
		t.Fatalf("partial pack: gotValue %v, expectedValue ErrPartialPackNeedsForce", err)
	}
	if fixture.stored(packID).Decision != store.ProposalPending {
		t.Fatal("refused partial pack changed")
	}
	if err := service.Accept(ctx, packID, AllowPartialPack); err != nil || fixture.stored(packID).Decision != store.ProposalAccepted {
		t.Fatalf("gotValue %v, expectedValue the partial pack accepted with the explicit option", err)
	}
}

func TestApp_chooseSetsOperatorPrimaryOnFullOnly(t *testing.T) {
	fixture := newDecisionFixture(t)
	service := fixture.service(crossseed.Native)
	ctx := context.Background()
	first, second := fixture.verifiedCandidate("aa"), fixture.verifiedCandidate("bb")
	firstID, secondID := fixture.proposal(first, store.ProposalFull), fixture.proposal(second, store.ProposalFull)
	annexID := fixture.proposal(second, store.ProposalPartialAnnex)
	if err := fixture.store.SetGroupPrimary(ctx, pipeline.GroupOf(fixture.candidate(first.ID)), firstID, store.PrimaryAuto); err != nil {
		t.Fatal(err)
	}

	if err := service.Choose(ctx, secondID); err != nil {
		t.Fatal(err)
	}

	chosen, previous := fixture.stored(secondID), fixture.stored(firstID)
	if !chosen.Primary || chosen.PrimarySource != store.PrimaryOperator || previous.Primary {
		t.Fatalf("gotValue chosen %+v previous %+v, expectedValue a single operator primary", chosen, previous)
	}
	if err := service.Choose(ctx, annexID); !errors.Is(err, ErrNotFull) {
		t.Fatalf("gotValue %v, expectedValue ErrNotFull", err)
	}
}

func TestApp_forceStartsOnlyLinkedSeededPartialAnnexAfterExplicitCall(t *testing.T) {
	fixture := newDecisionFixture(t)
	service := fixture.service(crossseed.Delegate)
	ctx := context.Background()
	annex := fixture.verifiedCandidate("aa")
	annexID := fixture.proposal(annex, store.ProposalPartialAnnex)

	if _, err := service.Force(ctx, annexID); !errors.Is(err, ErrNotForceable) || len(fixture.resumer.resumed) != 0 {
		t.Fatalf("pending: gotValue %v %v, expectedValue ErrNotForceable and nothing started", err, fixture.resumer.resumed)
	}
	fixture.seedPaused(annexID)

	result, err := service.Force(ctx, annexID)

	if err != nil || !result.Forced || !slices.Equal(fixture.resumer.resumed, []string{annex.Infohash}) {
		t.Fatalf("gotValue %+v %v resumed=%v, expectedValue the paused partial_annex started", result, err, fixture.resumer.resumed)
	}
	if !slices.Equal(result.Warnings, []string{"warning of partial_annex"}) {
		t.Fatalf("gotValue %v, expectedValue the proposal warning returned", result.Warnings)
	}
	progress, err := fixture.store.ListProposalProgress(ctx)
	if err != nil || progress[annexID].Step != store.StepForced {
		t.Fatalf("gotValue %+v %v, expectedValue the forced step", progress[annexID], err)
	}

	fullID := fixture.proposal(fixture.verifiedCandidate("bb"), store.ProposalFull)
	fixture.seedPaused(fullID)
	if _, err := service.Force(ctx, fullID); !errors.Is(err, ErrNotForceable) {
		t.Fatalf("full: gotValue %v, expectedValue ErrNotForceable", err)
	}
	packID := fixture.proposal(fixture.verifiedCandidate("cc"), store.ProposalPartialPack)
	fixture.seedPaused(packID)
	if _, err := service.Force(ctx, packID); !errors.Is(err, ErrPartialPackNeedsForce) {
		t.Fatalf("partial pack: gotValue %v, expectedValue ErrPartialPackNeedsForce", err)
	}
	if result, err := service.Force(ctx, packID, AllowPartialPack); err != nil || !result.Forced {
		t.Fatalf("partial pack with option: gotValue %+v %v, expectedValue forced", result, err)
	}
}

func TestApp_acceptGroupAcceptsAllVerifiedFullAndCrossSeedInNativeOnly(t *testing.T) {
	for _, behavior := range []crossseed.Behavior{crossseed.Native, crossseed.Delegate, crossseed.Disabled} {
		fixture := newDecisionFixture(t)
		ctx := context.Background()
		primary, other := fixture.verifiedCandidate("aa"), fixture.verifiedCandidate("bb")
		primaryID, otherFullID := fixture.proposal(primary, store.ProposalFull), fixture.proposal(other, store.ProposalFull)
		otherCrossSeedID := fixture.proposal(other, store.ProposalCrossSeed)
		groupID := pipeline.GroupOf(fixture.candidate(primary.ID))
		if err := fixture.store.SetGroupPrimary(ctx, groupID, primaryID, store.PrimaryAuto); err != nil {
			t.Fatal(err)
		}

		accepted, err := fixture.service(behavior).AcceptGroup(ctx, groupID)

		expectedValue := []int64{primaryID}
		if behavior == crossseed.Native {
			expectedValue = append(expectedValue, otherCrossSeedID)
		}
		if err != nil || !slices.Equal(accepted, expectedValue) || fixture.stored(otherFullID).Decision != store.ProposalPending {
			t.Fatalf("behavior %v: gotValue %v %v, expectedValue %v and the other full left pending", behavior, accepted, err, expectedValue)
		}
	}
}

// staleStore simule le refus de génération du store au moment de la décision.
type staleStore struct{ *store.Store }

func (staleStore) SetProposalDecision(context.Context, int64, store.ProposalDecision, time.Time) error {
	return store.ErrStaleGeneration
}

func TestApp_staleGenerationIsMappedToATypedMessage(t *testing.T) {
	fixture := newDecisionFixture(t)
	ctx := context.Background()
	proposalID := fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalFull)
	service := New(Deps{Store: staleStore{fixture.store}, Now: func() time.Time { return fixedNow }})

	err := service.Accept(ctx, proposalID)

	if !errors.Is(err, ErrStaleProposal) || !strings.Contains(err.Error(), "proposition périmée, revérifier") {
		t.Fatalf("gotValue %v, expectedValue the typed stale message", err)
	}
	if _, err := fixture.store.InvalidateCandidatesForFile(ctx, fixture.media.ID, fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service(crossseed.Delegate).Accept(ctx, proposalID); !errors.Is(err, ErrStaleProposal) {
		t.Fatalf("invalidated: gotValue %v, expectedValue ErrStaleProposal", err)
	}
}

func TestApp_acceptRefusesASecondTorrentPerFileWhenCrossSeedIsDisabled(t *testing.T) {
	fixture := newDecisionFixture(t)
	ctx := context.Background()
	firstID := fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalFull)
	secondID := fixture.proposal(fixture.verifiedCandidate("bb"), store.ProposalFull)
	service := fixture.service(crossseed.Disabled)
	if err := service.Accept(ctx, firstID); err != nil {
		t.Fatal(err)
	}
	if err := service.Accept(ctx, secondID); !errors.Is(err, ErrOneTorrentPerFile) {
		t.Fatalf("gotValue %v, expectedValue ErrOneTorrentPerFile", err)
	}
}

func TestApp_listProposalsReturnsDetailsForTheCliAndWeb(t *testing.T) {
	fixture := newDecisionFixture(t)
	candidate := fixture.verifiedCandidate("aa")
	proposalID := fixture.proposal(candidate, store.ProposalPartialAnnex)

	views, err := fixture.service(crossseed.Delegate).ListProposals(context.Background(), ProposalFilter{Kind: store.ProposalPartialAnnex, CurrentOnly: true})

	isExpected := err == nil && len(views) == 1 && views[0].ID == proposalID && views[0].CandidateName == candidate.Name &&
		views[0].CandidateSize == 40 && views[0].Current && views[0].Decision == store.ProposalPending
	if !isExpected {
		t.Fatalf("gotValue %+v %v, expectedValue the partial_annex view", views, err)
	}
}

func TestApp_actionsRegistryListsEveryServiceMethod(t *testing.T) {
	serviceType := reflect.TypeFor[Service]()
	registered := map[string]bool{}
	for _, action := range Actions() {
		if _, found := serviceType.MethodByName(action.Method); !found || action.Name == "" || action.Description == "" {
			t.Fatalf("gotValue action %+v, expectedValue a named action bound to a Service method", action)
		}
		registered[action.Method] = true
	}
	for methodIndex := range serviceType.NumMethod() {
		methodName := serviceType.Method(methodIndex).Name
		if methodName != "Actions" && !registered[methodName] {
			t.Fatalf("gotValue no action for %s, expectedValue every Service method registered", methodName)
		}
	}
}

func TestNewFromConfig_wiresAMinimalConfigAndNamesTheMissingComponent(t *testing.T) {
	settings := config.Config{
		ConfigDir: t.TempDir(), DBPath: "necromanciarr.db", ReadBytesPerSecond: 1 << 20, LinkType: "hardlink", CrossSeedMode: "off",
	}
	service, closer, err := NewFromConfig(context.Background(), settings)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })

	if _, err := service.RunStage(context.Background(), pipeline.StageMatch); !errors.Is(err, ErrNotConfigured) || !strings.Contains(err.Error(), "library provider") {
		t.Fatalf("gotValue %v, expectedValue ErrNotConfigured naming the library provider", err)
	}
	status, err := service.Status(context.Background())
	if err != nil || status.CrossSeedMode != "disabled" {
		t.Fatalf("gotValue %+v %v, expectedValue an empty status in disabled mode", status, err)
	}

	settings.LinkType = "bogus"
	if _, _, err := NewFromConfig(context.Background(), settings); err == nil || !strings.Contains(err.Error(), "linkType") {
		t.Fatalf("gotValue %v, expectedValue an error naming linkType", err)
	}
}
