package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"necromanciarr/internal/config"
	"necromanciarr/internal/crossseed"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
)

// trackerAEntry est l'entrée trackers du candidat de test ; l'URL porte une passkey factice jamais rendue.
var trackerAEntry = config.Tracker{Key: "tracker-a", Announce: []string{"https://tracker-a.example.com/fake-passkey-aaaa/announce"}}

func listedProposal(t *testing.T, service Service) Proposal {
	t.Helper()
	proposals, err := service.ListProposals(t.Context(), ProposalFilter{})
	if err != nil || len(proposals) != 1 {
		t.Fatalf("gotValue %+v %v, expectedValue one proposal", proposals, err)
	}
	return proposals[0]
}

func hasWarningCode(warnings []Warning, key i18n.Key) bool {
	for _, warning := range warnings {
		if warning.Code == string(key) {
			return true
		}
	}
	return false
}

func announceService(fixture *decisionFixture, language i18n.Language, trackers ...config.Tracker) Service {
	return New(Deps{Store: fixture.store, Config: config.Config{Trackers: trackers}, Translator: i18n.New(language, nil)})
}

func TestProposals_noAnnounceClearedWhenTrackerConfigured(t *testing.T) {
	fixture := newDecisionFixture(t)
	fixture.proposal(fixture.verifiedCandidateOn("aa", "Tracker-A"), store.ProposalFull)
	steps := []struct {
		name           string
		trackers       []config.Tracker
		expectedSource sources.AnnounceSource
	}{
		{name: "no trackers entry", expectedSource: sources.AnnounceNone},
		{name: "entry added", trackers: []config.Tracker{trackerAEntry}, expectedSource: sources.AnnounceConfig},
		{name: "entry removed", expectedSource: sources.AnnounceNone},
	}
	for _, step := range steps {
		service := announceService(fixture, i18n.English, step.trackers...)

		view := listedProposal(t, service)
		status, err := service.Status(t.Context())

		expectedSeedable := step.expectedSource != sources.AnnounceNone
		isExpected := err == nil && view.Details.Seedable != nil && *view.Details.Seedable == expectedSeedable && view.Details.AnnounceSource == string(step.expectedSource) &&
			hasWarningCode(view.Warnings, i18n.KeyWarningNoAnnounce) == !expectedSeedable && (status.NoAnnounce == 1) == !expectedSeedable
		if !isExpected {
			t.Fatalf("%s: gotValue details %+v warnings %+v status %d %v, expectedValue source %s without re-Verify",
				step.name, view.Details, view.Warnings, status.NoAnnounce, err, step.expectedSource)
		}
	}
}

func TestProposals_noAnnounceWarningIsTranslatedAndNamesTheTracker(t *testing.T) {
	fixture := newDecisionFixture(t)
	fixture.proposal(fixture.verifiedCandidateOn("aa", "Tracker-A"), store.ProposalPartialAnnex)
	expectedTexts := map[i18n.Language]string{
		i18n.French:  "aucune annonce pour le tracker Tracker-A : ajouter une entrée `trackers`",
		i18n.English: "no announce for tracker Tracker-A: add a `trackers` entry",
	}
	for language, expectedValue := range expectedTexts {
		view := listedProposal(t, announceService(fixture, language))
		gotValue := view.Warnings[len(view.Warnings)-1]
		if gotValue.Code != string(i18n.KeyWarningNoAnnounce) || gotValue.Text != expectedValue {
			t.Fatalf("language %s: gotValue %+v, expectedValue %q", language, gotValue, expectedValue)
		}
	}
}

func TestProposals_announceFieldsNeverCarryTheURL(t *testing.T) {
	fixture := newDecisionFixture(t)
	fixture.proposal(fixture.verifiedCandidateOn("aa", "tracker-a"), store.ProposalFull)

	encoded, err := json.Marshal(listedProposal(t, announceService(fixture, i18n.English, trackerAEntry)))
	if err != nil {
		t.Fatal(err)
	}

	text := string(encoded)
	if !strings.Contains(text, `"seedable":true`) || !strings.Contains(text, `"announce_source":"config"`) ||
		strings.Contains(text, "fake-passkey") || strings.Contains(text, "tracker-a.example.com") {
		t.Fatalf("gotValue %s, expectedValue seedable and the source name only", text)
	}
}

func TestProposals_sealedTorrentClearedOnReadOnceATrackerEntryCoversIt(t *testing.T) {
	fixture := newDecisionFixture(t)
	candidate := fixture.verifiedCandidateOn("aa", "Tracker-A")
	fixture.proposal(candidate, store.ProposalFull)
	if err := fixture.store.SaveCandidateSealed(context.Background(), candidate.ID, []byte{1, 2, 3}, fixedNow); err != nil {
		t.Fatal(err)
	}

	withoutEntry := listedProposal(t, announceService(fixture, i18n.English))
	_, keptWithoutEntry, err := fixture.store.CandidateSealed(context.Background(), candidate.ID)
	if err != nil || !keptWithoutEntry || withoutEntry.Details.AnnounceSource != string(sources.AnnounceIndexer) || withoutEntry.Details.Seedable == nil || !*withoutEntry.Details.Seedable {
		t.Fatalf("gotValue %+v kept=%v %v, expectedValue the indexer announce and the sealed torrent kept", withoutEntry.Details, keptWithoutEntry, err)
	}

	withEntry := listedProposal(t, announceService(fixture, i18n.English, trackerAEntry))
	_, keptWithEntry, err := fixture.store.CandidateSealed(context.Background(), candidate.ID)
	if err != nil || keptWithEntry || withEntry.Details.AnnounceSource != string(sources.AnnounceConfig) {
		t.Fatalf("gotValue %+v kept=%v %v, expectedValue the config announce and the sealed torrent cleared", withEntry.Details, keptWithEntry, err)
	}
}

func TestClearSealedAtStart_clearsOnlyTheTrackersAnEntryCovers(t *testing.T) {
	fixture := newDecisionFixture(t)
	covered := fixture.verifiedCandidateOn("aa", "Tracker-A")
	uncovered := fixture.verifiedCandidateOn("bb", "tracker-b")
	for _, candidate := range []store.Candidate{covered, uncovered} {
		if err := fixture.store.SaveCandidateSealed(context.Background(), candidate.ID, []byte{1, 2, 3}, fixedNow); err != nil {
			t.Fatal(err)
		}
	}

	if err := clearSealedAtStart(context.Background(), fixture.store, config.Config{Trackers: []config.Tracker{trackerAEntry}}); err != nil {
		t.Fatal(err)
	}

	_, coveredKept, coveredErr := fixture.store.CandidateSealed(context.Background(), covered.ID)
	_, uncoveredKept, uncoveredErr := fixture.store.CandidateSealed(context.Background(), uncovered.ID)
	if coveredErr != nil || uncoveredErr != nil || coveredKept || !uncoveredKept {
		t.Fatalf("gotValue covered kept=%v uncovered kept=%v (%v %v), expectedValue only the covered tracker cleared",
			coveredKept, uncoveredKept, coveredErr, uncoveredErr)
	}
}

func TestProposals_noAnnounceWarningNamesAnEmptyTrackerAsUnknown(t *testing.T) {
	fixture := newDecisionFixture(t)
	fixture.proposal(fixture.verifiedCandidate("aa"), store.ProposalFull)
	expectedTexts := map[i18n.Language]string{
		i18n.French:  "aucune annonce pour le tracker (inconnu) : ajouter une entrée `trackers`",
		i18n.English: "no announce for tracker (unknown): add a `trackers` entry",
	}
	for language, expectedValue := range expectedTexts {
		view := listedProposal(t, announceService(fixture, language))
		if gotValue := view.Warnings[len(view.Warnings)-1].Text; gotValue != expectedValue {
			t.Fatalf("language %s: gotValue %q, expectedValue %q", language, gotValue, expectedValue)
		}
	}
}

// busyClearer simule une base occupée au moment d'effacer les scellés ; son erreur cite un secret de la configuration.
type busyClearer struct {
	Store
}

const fakeAuthToken = "fake-auth-token-0000-busy"

func (busyClearer) ClearSealedForTrackers(context.Context, []string) (int, error) {
	return 0, errors.New("database is locked while serving " + fakeAuthToken)
}

func TestProposals_readsDespiteABusyDatabaseWhenClearingSealed(t *testing.T) {
	fixture := newDecisionFixture(t)
	candidate := fixture.verifiedCandidateOn("aa", "Tracker-A")
	fixture.proposal(candidate, store.ProposalFull)
	if err := fixture.store.SaveCandidateSealed(context.Background(), candidate.ID, []byte{1, 2, 3}, fixedNow); err != nil {
		t.Fatal(err)
	}
	var logged bytes.Buffer
	service := New(Deps{
		Store: busyClearer{Store: fixture.store}, Translator: i18n.New(i18n.English, nil),
		Config: config.Config{Trackers: []config.Tracker{trackerAEntry}, AuthToken: fakeAuthToken},
		Logger: slog.New(slog.NewTextHandler(&logged, nil)),
	})

	view := listedProposal(t, service)
	_, statusErr := service.Status(t.Context())

	if statusErr != nil || view.Details.AnnounceSource != string(sources.AnnounceConfig) {
		t.Fatalf("gotValue %+v status error %v, expectedValue the proposals read and the config announce", view.Details, statusErr)
	}
	if text := logged.String(); !strings.Contains(text, "code="+LogCodeSealedClearFailed) || strings.Contains(text, fakeAuthToken) {
		t.Fatalf("gotValue log %q, expectedValue a coded, redacted line", text)
	}
}

func TestProposalDetails_includeSource(t *testing.T) {
	fixture := newDecisionFixture(t)
	fixture.proposal(fixture.verifiedCandidateFrom("aa", "tracker-a", ""), store.ProposalFull)
	fixture.proposal(fixture.verifiedCandidateFrom("bb", "tracker-b", "torznab:b"), store.ProposalFull)
	service := fixture.service(crossseed.Disabled)

	all, allErr := service.ListProposals(t.Context(), ProposalFilter{})
	fromIndexer, filterErr := service.ListProposals(t.Context(), ProposalFilter{Source: "torznab:b"})
	_, invalidErr := service.ListProposals(t.Context(), ProposalFilter{Source: "https://indexer.example.com"})

	isExpected := allErr == nil && filterErr == nil && len(all) == 2 && all[0].Details.Source == "provider" &&
		all[1].Details.Source == "torznab:b" && len(fromIndexer) == 1 && fromIndexer[0].CandidateName == "Movie bb" &&
		errors.Is(invalidErr, config.ErrUsage)
	if !isExpected {
		t.Fatalf("valeurObtenue %+v %v / %+v %v / %v, valeurAttendue sources provider and torznab:b, filter keeping one, usage error",
			all, allErr, fromIndexer, filterErr, invalidErr)
	}
	encoded, _ := json.Marshal(all[1].Details)
	if !strings.Contains(string(encoded), `"source":"torznab:b"`) {
		t.Fatalf("valeurObtenue %s, valeurAttendue the source field in details", encoded)
	}
}
