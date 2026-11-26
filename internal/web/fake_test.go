package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
)

// Valeurs de test à faible entropie (gitleaks) ; testHost est l'en-tête Host d'un navigateur sur l'adresse d'écoute.
const (
	testToken      = "web-test-token"
	testListen     = "127.0.0.1:8787"
	testHost       = "127.0.0.1:8787"
	requestedWith  = "necromanciarr"
	jsonType       = "application/json"
	pollInterval   = 5 * time.Millisecond
	pollAttempts   = 400
	indexBody      = "<!doctype html><title>built index</title>"
	assetBody      = "console.log('app')"
	placeholderTag = ".placeholder"
)

// fakeService enregistre chaque appel ; errors donne l'erreur d'une méthode, forceErrors celle de Force par id,
// stageGate bloque RunStage jusqu'à sa fermeture (ou jusqu'à l'annulation du contexte du job).
type fakeService struct {
	mutex       sync.Mutex
	calls       []string
	proposals   []app.Proposal
	status      app.Status
	errors      map[string]error
	forceErrors map[int64]error
	stageGate   chan struct{}
	lastFilter  app.ProposalFilter
	lastLinks   app.LinkFilter
	linkPage    app.LinkPage
	sourceRows  []app.SourceRow
	volumeRows  []app.VolumeRow
	// language est la langue du Translator (anglais si vide), pour comparer fr et en.
	language i18n.Language
	// panicValue fait paniquer RunStage avec cette valeur (job qui plante).
	panicValue any
	// configView est rendue par ConfigView ; les écritures de configuration retiennent leurs derniers arguments.
	configView       app.ConfigView
	lastSettingValue json.RawMessage
	lastList         string
	lastItemID       string
	lastElement      map[string]any
}

func (fake *fakeService) record(method string, arguments ...any) error {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.calls = append(fake.calls, strings.TrimSpace(method+" "+fmt.Sprint(arguments...)))
	return fake.errors[method]
}

func (fake *fakeService) called(call string) bool {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return slices.ContainsFunc(fake.calls, func(recorded string) bool {
		return recorded == call || strings.HasPrefix(recorded, call+" ")
	})
}

func (fake *fakeService) RunStage(ctx context.Context, stage pipeline.Stage) (pipeline.Result, error) {
	err := fake.record("RunStage", stage)
	if fake.panicValue != nil {
		panic(fake.panicValue)
	}
	if fake.stageGate != nil {
		select {
		case <-fake.stageGate:
		case <-ctx.Done():
			return pipeline.Result{}, ctx.Err()
		}
	}
	return pipeline.Result{Stage: stage, Processed: 3}, err
}

func (fake *fakeService) Status(context.Context) (app.Status, error) {
	return fake.status, fake.record("Status")
}

func (fake *fakeService) ListProposals(_ context.Context, filter app.ProposalFilter) ([]app.Proposal, error) {
	err := fake.record("ListProposals")
	fake.mutex.Lock()
	fake.lastFilter = filter
	fake.mutex.Unlock()
	return fake.proposals, err
}

func (fake *fakeService) Accept(_ context.Context, proposalID int64, options ...app.DecisionOption) error {
	return fake.record("Accept", proposalID, options)
}

func (fake *fakeService) Reject(_ context.Context, proposalID int64) error {
	return fake.record("Reject", proposalID)
}

func (fake *fakeService) Choose(_ context.Context, proposalID int64) error {
	return fake.record("Choose", proposalID)
}

func (fake *fakeService) Force(_ context.Context, proposalID int64) (app.ForceResult, error) {
	if err := fake.record("Force", proposalID); err != nil {
		return app.ForceResult{}, err
	}
	if err := fake.forceErrors[proposalID]; err != nil {
		return app.ForceResult{ProposalID: proposalID}, err
	}
	return app.ForceResult{ProposalID: proposalID, Forced: true, Warnings: []app.Warning{{Code: "warning.test", Text: "pieces will be rewritten"}}}, nil
}

func (fake *fakeService) AcceptGroup(_ context.Context, groupID int64) ([]int64, error) {
	return []int64{groupID * 10}, fake.record("AcceptGroup", groupID)
}

func (fake *fakeService) Doctor(_ context.Context, options app.DoctorOptions) (doctor.Report, error) {
	err := fake.record("Doctor", options.CheckIDs)
	return doctor.Report{Checks: []doctor.Outcome{{ID: "config", TitleKey: i18n.KeyDoctorTitleConfig, Result: doctor.Result{Code: doctor.CodeOK}}}}, err
}

func (fake *fakeService) DedupePlan(context.Context) ([]app.DedupeProposal, error) {
	return []app.DedupeProposal{}, fake.record("DedupePlan")
}

func (fake *fakeService) DedupeAccept(_ context.Context, proposalID int64, options ...app.DecisionOption) (app.MaintenanceResult, error) {
	err := fake.record("DedupeAccept", proposalID, options)
	return app.MaintenanceResult{ProposalID: proposalID, Applied: true}, err
}

func (fake *fakeService) RepairScan(_ context.Context, directory string) ([]app.RepairProposal, error) {
	return []app.RepairProposal{}, fake.record("RepairScan", directory)
}

func (fake *fakeService) RepairAccept(_ context.Context, proposalID int64, options ...app.DecisionOption) (app.MaintenanceResult, error) {
	err := fake.record("RepairAccept", proposalID, options)
	return app.MaintenanceResult{ProposalID: proposalID, Applied: true}, err
}

func (fake *fakeService) ListLinks(_ context.Context, filter app.LinkFilter) (app.LinkPage, error) {
	err := fake.record("ListLinks", filter)
	fake.mutex.Lock()
	fake.lastLinks = filter
	fake.mutex.Unlock()
	return fake.linkPage, err
}

func (fake *fakeService) ListSources(context.Context) ([]app.SourceRow, error) {
	return fake.sourceRows, fake.record("ListSources")
}

func (fake *fakeService) TestSource(_ context.Context, name string) (app.SourceTestResult, error) {
	err := fake.record("TestSource", name)
	return app.SourceTestResult{Source: "torznab:" + name, Result: app.SourceTestFailed, Error: "indexer_access_denied"}, err
}

func (fake *fakeService) ListVolumes(context.Context) ([]app.VolumeRow, error) {
	return fake.volumeRows, fake.record("ListVolumes")
}

func (fake *fakeService) ProbeVolume(_ context.Context, name string) (app.VolumeProbeResult, error) {
	err := fake.record("ProbeVolume", name)
	return app.VolumeProbeResult{Volume: name, Result: app.VolumeProbeFailed, Error: "host_key_unknown"}, err
}

func (fake *fakeService) VolumeCommands(_ context.Context, name string) (app.VolumeCommands, error) {
	err := fake.record("VolumeCommands", name)
	return app.VolumeCommands{Volume: name, Lines: []string{"stat\t--version"}, GuardScript: app.GuardScriptPath}, err
}

func (fake *fakeService) ConfigView(context.Context) (app.ConfigView, error) {
	return fake.configView, fake.record("ConfigView")
}

func (fake *fakeService) SetSetting(_ context.Context, key string, value json.RawMessage) error {
	fake.lastSettingValue = value
	return fake.record("SetSetting", key)
}

func (fake *fakeService) UnsetSetting(_ context.Context, key string) error {
	return fake.record("UnsetSetting", key)
}

func (fake *fakeService) PutItem(_ context.Context, list, itemID string, element map[string]any) error {
	fake.lastList, fake.lastItemID, fake.lastElement = list, itemID, element
	return fake.record("PutItem", list, itemID)
}

func (fake *fakeService) DeleteItem(_ context.Context, list, itemID string) error {
	fake.lastList, fake.lastItemID = list, itemID
	return fake.record("DeleteItem", list, itemID)
}

func (fake *fakeService) UnsetItem(_ context.Context, list, itemID string) error {
	fake.lastList, fake.lastItemID = list, itemID
	return fake.record("UnsetItem", list, itemID)
}

func (fake *fakeService) Actions() []app.Action { return app.Actions(fake.Translator()) }

func (fake *fakeService) Translator() i18n.Translator {
	if fake.language == "" {
		return i18n.New(i18n.English, nil)
	}
	return i18n.New(fake.language, nil)
}

// fakeHealth répond à Ping avec err et retient l'échéance du contexte reçu.
type fakeHealth struct {
	err      error
	deadline time.Time
}

func (health *fakeHealth) Ping(ctx context.Context) error {
	health.deadline, _ = ctx.Deadline()
	return health.err
}

var errDatabaseDown = errors.New("database is down")

func loopbackSettings() config.Config { return config.Config{ListenAddr: testListen} }

func tokenSettings() config.Config {
	return config.Config{ListenAddr: "0.0.0.0:8787", AuthToken: testToken}
}

func builtAssets() fstest.MapFS {
	return fstest.MapFS{
		"index.html":    {Data: []byte(indexBody)},
		"assets/app.js": {Data: []byte(assetBody)},
	}
}

// newTestHandler construit le handler ; à la fin du test, le contexte du serveur est annulé et les jobs attendus.
func newTestHandler(t *testing.T, fake *fakeService, settings config.Config) *Handler {
	t.Helper()
	return newTestHandlerWith(t, fake, &fakeHealth{}, settings, builtAssets())
}

func newTestHandlerWith(t *testing.T, fake *fakeService, health HealthChecker, settings config.Config, assets fs.FS) *Handler {
	t.Helper()
	serverContext, cancel := context.WithCancel(context.Background())
	handler := NewHandlerWithAssets(serverContext, fake, health, settings, assets)
	t.Cleanup(func() {
		cancel()
		handler.Wait()
	})
	return handler
}

// apiRequest prépare une requête de navigateur de même origine : Host d'écoute, JSON et en-tête X-Requested-With.
func apiRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Host = testHost
	if method != http.MethodGet && method != http.MethodHead {
		request.Header.Set("Content-Type", jsonType)
		request.Header.Set("X-Requested-With", requestedWith)
	}
	return request
}

func serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func readBody(t *testing.T, recorder *httptest.ResponseRecorder) string {
	t.Helper()
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
