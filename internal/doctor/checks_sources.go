package doctor

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/prowlarr"
	"necromanciarr/internal/torznab"
)

const (
	// IDProwlarr identifie le contrôle de Prowlarr ; celui d'un indexeur est « torznab:<nom> ».
	IDProwlarr         = "prowlarr"
	torznabCheckPrefix = "torznab:"
	noModes            = "-"
)

// CapsReader lit les capacités d'un indexeur Torznab : la seule requête que doctor lui adresse, qui ne compte ni
// recherche ni téléchargement.
type CapsReader interface {
	Caps(ctx context.Context) (torznab.Caps, error)
}

// IndexerLister liste les indexeurs de l'API native de Prowlarr.
type IndexerLister interface {
	Indexers(ctx context.Context) ([]prowlarr.Indexer, error)
}

func newTorznabCapsReader(indexer config.TorznabIndexer) (CapsReader, error) {
	return torznab.New(torznab.Endpoint{
		Name: indexer.Name, URL: indexer.URL, MinInterval: indexer.MinInterval, Timeout: indexer.Timeout,
	}, torznab.Deps{})
}

func newProwlarrLister(rawURL string) (IndexerLister, error) { return prowlarr.New(rawURL, nil) }

// sourceChecks crée un contrôle par indexeur Torznab et un pour Prowlarr ; aucun sans configuration.
func sourceChecks(settings config.Config, deps Deps) []Check {
	var checks []Check
	for _, indexer := range settings.Torznab {
		checks = append(checks, Check{
			ID:          torznabCheckPrefix + indexer.Name,
			TitleKey:    i18n.KeyDoctorTitleTorznab,
			TitleParams: map[string]string{"name": indexer.Name},
			Run:         func(ctx context.Context) Result { return checkTorznab(ctx, indexer, settings.SourceOrder, deps) },
		})
	}
	if settings.Prowlarr != "" {
		checks = append(checks, newCheck(IDProwlarr, i18n.KeyDoctorTitleProwlarr,
			func(ctx context.Context) Result { return checkProwlarr(ctx, settings, deps) }))
	}
	return checks
}

// checkTorznab ne lit que les capacités. Un indexeur absent de sourceOrder (vide : configuration non chargée par Load)
// ne sera jamais interrogé : avertissement, pas un état vert.
func checkTorznab(ctx context.Context, indexer config.TorznabIndexer, sourceOrder []string, deps Deps) Result {
	reader, err := deps.NewCapsReader(indexer)
	if err != nil {
		return Result{Status: Fail, Code: CodeConfigInvalid, Detail: message(i18n.KeyDoctorTorznabBuildFailed, "error", err.Error())}
	}
	caps, err := deps.CapsCache.caps(ctx, indexer, reader)
	if err != nil {
		return torznabFailure(err)
	}
	var collected findings
	if hasMediaMode(caps) {
		collected.add(OK, CodeTorznabOK, message(i18n.KeyDoctorTorznabOK, "modes", modesText(caps)), i18n.Message{})
	} else {
		collected.add(Warn, CodeTorznabNoMediaMode, message(i18n.KeyDoctorTorznabNoMediaMode, "modes", modesText(caps)),
			message(i18n.KeyDoctorTorznabNoMediaModeHint))
	}
	if len(sourceOrder) > 0 && !slices.Contains(sourceOrder, config.SourceTorznabPrefix+indexer.Name) {
		collected.add(Warn, CodeTorznabNotInSourceOrder, message(i18n.KeyDoctorTorznabNotInSourceOrder, "name", indexer.Name),
			message(i18n.KeyDoctorTorznabNotInSourceOrderHint, "name", indexer.Name))
	}
	return collected.result()
}

// torznabFailure traduit une erreur du client ; les erreurs du client ne contiennent ni URL ni clé.
func torznabFailure(err error) Result {
	switch {
	case errors.Is(err, torznab.ErrAccessDenied):
		return Result{
			Status: Fail, Code: CodeTorznabAccessDenied,
			Detail: message(i18n.KeyDoctorTorznabAccessDenied), Hint: message(i18n.KeyDoctorTorznabAccessDeniedHint),
		}
	case errors.Is(err, torznab.ErrHostileResponse):
		return Result{
			Status: Fail, Code: CodeTorznabHostileResponse,
			Detail: message(i18n.KeyDoctorTorznabHostile), Hint: message(i18n.KeyDoctorTorznabHostileHint),
		}
	case errors.Is(err, torznab.ErrBadResponse):
		return Result{
			Status: Fail, Code: CodeTorznabBadResponse,
			Detail: message(i18n.KeyDoctorTorznabBadResponse, "error", err.Error()), Hint: message(i18n.KeyDoctorTorznabBadResponseHint),
		}
	}
	return Result{
		Status: Fail, Code: CodeTorznabUnreachable,
		Detail: message(i18n.KeyDoctorTorznabUnreachable, "error", err.Error()), Hint: message(i18n.KeyDoctorTorznabUnreachableHint),
	}
}

func hasMediaMode(caps torznab.Caps) bool {
	_, hasMovie := caps.Modes["movie"]
	_, hasTV := caps.Modes["tvsearch"]
	return hasMovie || hasTV
}

func modesText(caps torznab.Caps) string {
	modes := make([]string, 0, len(caps.Modes))
	for mode := range caps.Modes {
		modes = append(modes, mode)
	}
	if len(modes) == 0 {
		return noModes
	}
	slices.Sort(modes)
	return strings.Join(modes, ", ")
}

func checkProwlarr(ctx context.Context, settings config.Config, deps Deps) Result {
	lister, err := deps.NewIndexerLister(settings.Prowlarr)
	if err != nil {
		return Result{Status: Fail, Code: CodeConfigInvalid, Detail: message(i18n.KeyDoctorProwlarrBuildFailed, "error", err.Error())}
	}
	indexers, err := lister.Indexers(ctx)
	if err != nil {
		return prowlarrFailure(err)
	}
	var collected findings
	for _, configured := range settings.Torznab {
		identifier, hasIdentifier := configured.ProwlarrID()
		if !hasIdentifier || !servedByProwlarr(configured.URL, settings.Prowlarr) {
			continue
		}
		if detail, faulty := indexerProblem(configured.Name, identifier, indexers); faulty {
			hint := i18n.Message{}
			if len(collected.details) == 0 {
				hint = message(i18n.KeyDoctorProwlarrIndexerHint)
			}
			collected.add(Warn, CodeProwlarrIndexerMissingOrDisabled, detail, hint)
		}
	}
	if len(collected.details) > 0 {
		return collected.result()
	}
	return Result{
		Status: OK, Code: CodeProwlarrOK,
		Detail: message(i18n.KeyDoctorProwlarrOK, "count", strconv.Itoa(len(indexers)), "enabled", strconv.Itoa(countEnabled(indexers))),
	}
}

// indexerProblem dit si l'indexeur configuré est absent de Prowlarr ou désactivé, avec le constat à afficher.
func indexerProblem(name string, identifier int, indexers []prowlarr.Indexer) (i18n.Message, bool) {
	position := slices.IndexFunc(indexers, func(candidate prowlarr.Indexer) bool { return candidate.ID == identifier })
	id := strconv.Itoa(identifier)
	switch {
	case position < 0:
		return message(i18n.KeyDoctorProwlarrIndexerMissing, "name", name, "id", id), true
	case !indexers[position].Enabled:
		return message(i18n.KeyDoctorProwlarrIndexerDisabled, "name", name, "id", id, "prowlarr_name", indexers[position].Name), true
	}
	return i18n.Message{}, false
}

func countEnabled(indexers []prowlarr.Indexer) int {
	return len(slices.DeleteFunc(slices.Clone(indexers), func(candidate prowlarr.Indexer) bool { return !candidate.Enabled }))
}

func prowlarrFailure(err error) Result {
	switch {
	case errors.Is(err, prowlarr.ErrAccessDenied):
		return Result{
			Status: Fail, Code: CodeProwlarrAccessDenied,
			Detail: message(i18n.KeyDoctorProwlarrAccessDenied), Hint: message(i18n.KeyDoctorProwlarrAccessDeniedHint),
		}
	case errors.Is(err, prowlarr.ErrBadResponse):
		return Result{
			Status: Fail, Code: CodeProwlarrBadResponse,
			Detail: message(i18n.KeyDoctorProwlarrBadResponse, "error", err.Error()), Hint: message(i18n.KeyDoctorProwlarrURLHint),
		}
	}
	return Result{
		Status: Fail, Code: CodeProwlarrUnreachable,
		Detail: message(i18n.KeyDoctorProwlarrUnreachable, "error", err.Error()), Hint: message(i18n.KeyDoctorProwlarrURLHint),
	}
}

// servedByProwlarr dit si le flux vient de l'instance Prowlarr configurée : même origine et, sous sa base d'URL, un
// chemin qui la prolonge. Un flux d'un autre hôte (Jackett, autre Prowlarr) n'est pas évalué : son identifiant ne
// désigne rien dans cette liste.
func servedByProwlarr(indexerURL, prowlarrURL string) bool {
	indexerParsed, indexerErr := url.Parse(indexerURL)
	prowlarrParsed, prowlarrErr := url.Parse(prowlarrURL)
	if indexerErr != nil || prowlarrErr != nil || !torznab.SameOrigin(indexerParsed, prowlarrParsed) {
		return false
	}
	basePath := strings.TrimSuffix(prowlarrParsed.Path, "/")
	return basePath == "" || indexerParsed.Path == basePath || strings.HasPrefix(indexerParsed.Path, basePath+"/")
}
