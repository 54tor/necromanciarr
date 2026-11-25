package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/sources"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torznab"
)

// Sortes de source de sources list (contrat machine, jamais traduit).
const (
	SourceTypeProvider = "provider"
	SourceTypeTorznab  = "torznab"
)

// États d'une source dans sources list : ok, blocked (Retry-After en cours dans ce processus), exhausted (un plafond
// du jour atteint), disabled (configurée mais hors de la chaîne : sans libraryApi, ou absente de sourceOrder).
const (
	SourceStateOK        = "ok"
	SourceStateBlocked   = "blocked"
	SourceStateExhausted = "exhausted"
	SourceStateDisabled  = "disabled"
)

// Résultats de sources test.
const (
	SourceTestOK     = "ok"
	SourceTestFailed = "failed"
)

var (
	// ErrSourceNotFound : aucune source configurée ne porte ce nom.
	ErrSourceNotFound = errors.New("app: source not found")
	// ErrSourceTestRunning : un test du même indexeur est déjà en cours dans ce processus.
	ErrSourceTestRunning = errors.New("app: a test of this source is already running")
	// ErrSourceNotTestable : le fournisseur n'a pas de test de source ; doctor contrôle libraryApi.
	ErrSourceNotTestable = errors.New("app: the library provider is checked by doctor, not by sources test")
)

// SourceUsage est l'usage d'un indexeur sur les dernières 24 h face à son plafond du jour.
type SourceUsage struct {
	Used int `json:"used"`
	Cap  int `json:"cap"`
}

// SourceRow est une ligne de sources list : Order est le rang dans la chaîne (0 : hors chaîne), Queries et Downloads
// sont nil pour le fournisseur (sans quota), LastError le code stable de la dernière erreur vue par ce processus,
// CapsCached dit si le client de ce processus garde en cache des capacités valables.
type SourceRow struct {
	Order      int          `json:"order"`
	Source     string       `json:"source"`
	Type       string       `json:"type"`
	Tracker    string       `json:"tracker"`
	State      string       `json:"state"`
	Queries    *SourceUsage `json:"queries"`
	Downloads  *SourceUsage `json:"downloads"`
	LastError  string       `json:"last_error"`
	CapsCached bool         `json:"caps_cached"`
}

// SourceTestResult rend un sources test : Result ok ou failed, Error le code stable de l'échec.
type SourceTestResult struct {
	Source string `json:"source"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// sourceProbe est la part de *sources.Chain que sources list et sources test lisent.
type sourceProbe interface {
	Health(indexer sources.ID) sources.Health
	TestIndexer(ctx context.Context, indexer sources.ID) error
}

func (svc *service) probe() (sourceProbe, bool) {
	probe, hasProbe := svc.deps.Stages.Sources.(sourceProbe)
	return probe, hasProbe
}

func (svc *service) ListSources(ctx context.Context) ([]SourceRow, error) {
	settings := svc.deps.Config
	probe, hasChain := svc.probe()
	order := sourceOrderOf(settings)
	rows := make([]SourceRow, 0, len(order)+len(settings.Torznab))
	for position, identifier := range order {
		row, err := svc.sourceRow(ctx, identifier, probe, hasChain)
		if err != nil {
			return nil, err
		}
		row.Order = position + 1
		rows = append(rows, row)
	}
	for _, indexer := range settings.Torznab {
		identifier := indexerID(indexer)
		if slices.Contains(order, identifier) {
			continue
		}
		row, err := svc.indexerRow(ctx, indexer, nil)
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (svc *service) sourceRow(ctx context.Context, identifier sources.ID, probe sourceProbe, hasChain bool) (SourceRow, error) {
	if identifier == sources.ProviderID {
		state := SourceStateOK
		if !hasChain {
			state = SourceStateDisabled
		}
		return SourceRow{Source: string(identifier), Type: SourceTypeProvider, State: state}, nil
	}
	indexer, found := svc.indexerNamed(identifier)
	if !found {
		return SourceRow{Source: string(identifier), Type: SourceTypeTorznab, State: SourceStateDisabled}, nil
	}
	var health *sources.Health
	if hasChain {
		observed := probe.Health(identifier)
		health = &observed
	}
	return svc.indexerRow(ctx, indexer, health)
}

// indexerRow décrit un indexeur ; health nil : hors de la chaîne de ce processus (disabled).
func (svc *service) indexerRow(ctx context.Context, indexer config.TorznabIndexer, health *sources.Health) (SourceRow, error) {
	identifier := indexerID(indexer)
	queries, err := svc.usageOf(ctx, identifier, sources.KindQuery, indexer.MaxQueriesPerDay)
	if err != nil {
		return SourceRow{}, err
	}
	downloads, err := svc.usageOf(ctx, identifier, sources.KindDownload, indexer.MaxDownloadsPerDay)
	if err != nil {
		return SourceRow{}, err
	}
	row := SourceRow{
		Source: string(identifier), Type: SourceTypeTorznab, State: SourceStateDisabled, Queries: queries, Downloads: downloads,
		Tracker: svc.trackerLabelOf(indexer.Tracker),
	}
	if health == nil {
		return row, nil
	}
	row.LastError, row.CapsCached = sourceErrorCode(health.LastError), health.CapsCached
	isExhausted := queries.Used >= queries.Cap || downloads.Used >= downloads.Cap
	switch {
	case health.BlockedFor > 0:
		row.State = SourceStateBlocked
	case isExhausted:
		row.State = SourceStateExhausted
	default:
		row.State = SourceStateOK
	}
	return row, nil
}

// trackerLabelOf rend le tracker déclaré ; une valeur en forme d'URL (annonce saisie par erreur) n'est rendue que
// schéma://hôte/***.
func (svc *service) trackerLabelOf(tracker string) string {
	if strings.Contains(tracker, "://") {
		return config.RedactAnnounce(tracker)
	}
	return config.RedactText(tracker, svc.deps.Config.KnownSecrets()...)
}

func (svc *service) usageOf(ctx context.Context, identifier sources.ID, kind string, dailyCap int) (*SourceUsage, error) {
	used, err := svc.deps.Store.CountIndexerUsage(ctx, string(identifier), kind, svc.now().Add(-store.IndexerUsageWindow))
	if err != nil {
		return nil, err
	}
	return &SourceUsage{Used: used, Cap: dailyCap}, nil
}

func (svc *service) indexerNamed(identifier sources.ID) (config.TorznabIndexer, bool) {
	for _, indexer := range svc.deps.Config.Torznab {
		if indexerID(indexer) == identifier {
			return indexer, true
		}
	}
	return config.TorznabIndexer{}, false
}

func (svc *service) TestSource(ctx context.Context, name string) (SourceTestResult, error) {
	identifier := sourceIDOf(name)
	if identifier == sources.ProviderID {
		return SourceTestResult{}, ErrSourceNotTestable
	}
	if _, found := svc.indexerNamed(identifier); !found {
		return SourceTestResult{}, ErrSourceNotFound
	}
	probe, hasChain := svc.probe()
	if !hasChain {
		return SourceTestResult{}, fmt.Errorf("%w: source chain (libraryApi)", ErrNotConfigured)
	}
	if _, alreadyRunning := svc.sourceTests.LoadOrStore(identifier, struct{}{}); alreadyRunning {
		return SourceTestResult{}, ErrSourceTestRunning
	}
	defer svc.sourceTests.Delete(identifier)
	err := probe.TestIndexer(ctx, identifier)
	if errors.Is(err, sources.ErrUnknownIndexer) {
		return SourceTestResult{}, fmt.Errorf("%w: %s is not in sourceOrder", ErrNotConfigured, identifier)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return SourceTestResult{}, ctxErr
	}
	if err != nil {
		return SourceTestResult{Source: string(identifier), Result: SourceTestFailed, Error: sourceErrorCode(err)}, nil
	}
	return SourceTestResult{Source: string(identifier), Result: SourceTestOK}, nil
}

// sourceIDOf accepte l'identifiant de la chaîne (provider, torznab:<nom>) ou le seul nom de l'indexeur.
func sourceIDOf(name string) sources.ID {
	if name == string(sources.ProviderID) || strings.Contains(name, ":") {
		return sources.ID(name)
	}
	return sources.ID(config.SourceTorznabPrefix + name)
}

// isSourceID dit si value a la forme d'une source de la chaîne.
func isSourceID(value string) bool {
	isIndexer := strings.HasPrefix(value, config.SourceTorznabPrefix) && len(value) > len(config.SourceTorznabPrefix)
	return value == string(sources.ProviderID) || isIndexer
}

func indexerID(indexer config.TorznabIndexer) sources.ID {
	return sources.ID(config.SourceTorznabPrefix + indexer.Name)
}

// sourceErrorCode réduit une erreur d'indexeur à un code de raison stable : le texte de l'erreur (URL, clé, corps de
// réponse) n'en sort jamais. Une erreur sans type connu (transport, délai) vaut indexer_unavailable.
func sourceErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, sources.ErrBudgetExhausted):
		return pipeline.ReasonIndexerBudgetExhausted
	case errors.Is(err, torznab.ErrForeignDownloadHost):
		return pipeline.ReasonForeignDownloadHost
	case errors.Is(err, torznab.ErrHostileResponse), errors.Is(err, torznab.ErrBadResponse):
		return pipeline.ReasonIndexerHostileResponse
	case errors.Is(err, torznab.ErrAccessDenied):
		return pipeline.ReasonIndexerAccessDenied
	default:
		return pipeline.ReasonIndexerUnavailable
	}
}
