package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"necromanciarr/internal/app"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/i18n"
)

// route relie une méthode et un chemin de l'API à l'action du registre qu'elle appelle (parité CLI et web). Une route
// sans action (suivi des jobs) porte sa justification.
type route struct {
	method        string
	path          string
	action        string
	justification string
	handle        endpointFunc
}

func (apiRoute route) pattern() string { return apiRoute.method + " " + apiRoute.path }

// apiRoutes est la table de l'API ; les actions longues (étapes, dedupe, repair) répondent 202 avec un job.
var apiRoutes = []route{
	{http.MethodGet, "/api/status", "status", "", getStatus},
	{http.MethodGet, "/api/proposals", "proposals list", "", listProposals},
	{http.MethodPost, "/api/proposals/{id}/accept", "proposals accept", "", acceptProposal},
	{http.MethodPost, "/api/proposals/{id}/reject", "proposals reject", "", rejectProposal},
	{http.MethodPost, "/api/proposals/{id}/choose", "proposals choose", "", chooseCandidate},
	{http.MethodPost, "/api/proposals/{id}/force", "proposals force", "", forceProposal},
	{http.MethodPost, "/api/groups/{id}/accept", "proposals accept-group", "", acceptGroup},
	{http.MethodPost, "/api/stages/{stage}/run", "run", "", runStage},
	{http.MethodPost, "/api/dedupe/plan", "dedupe plan", "", planDedupe},
	{http.MethodPost, "/api/dedupe/{id}/apply", "dedupe apply", "", applyDedupe},
	{http.MethodPost, "/api/repair/scan", "repair scan", "", scanRepair},
	{http.MethodPost, "/api/repair/{id}/apply", "repair apply", "", applyRepair},
	{http.MethodGet, "/api/links", "links list", "", listLinks},
	{http.MethodPost, "/api/doctor/run", "doctor", "", runDoctor},
	{http.MethodPost, "/api/doctor/checks/{checkId}", "doctor", "", runDoctorCheck},
	{http.MethodGet, "/api/sources", "sources list", "", listSources},
	{http.MethodPost, "/api/sources/{name}/test", "sources test", "", testSource},
	{http.MethodGet, "/api/jobs", "", "follow the long actions started by the action routes", listJobs},
	{http.MethodGet, "/api/jobs/{id}", "", "follow one long action started by an action route", getJob},
}

// Paramètres de requête de la liste des propositions (mêmes filtres que la CLI).
const (
	queryDecision    = "decision"
	queryKind        = "kind"
	queryGroup       = "group"
	queryCurrentOnly = "current_only"
	querySource      = "source"
	queryState       = "state"
	queryLimit       = "limit"
	queryOffset      = "offset"
)

func pathID(request *http.Request, name string) (int64, error) {
	identifier, err := strconv.ParseInt(request.PathValue(name), 10, 64)
	if err != nil || identifier <= 0 {
		return 0, newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebPositiveInteger, name))
	}
	return identifier, nil
}

// requireKnownQuery refuse un paramètre inconnu plutôt que de l'ignorer (un filtre mal nommé ne filtrerait rien).
func requireKnownQuery(query url.Values, known ...string) error {
	for name := range query {
		if !slices.Contains(known, name) {
			return newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebUnknownQuery, strconv.Quote(name)))
		}
	}
	return nil
}

func getStatus(handler *Handler, request *http.Request) (any, int, error) {
	status, err := handler.service.Status(request.Context())
	return status, http.StatusOK, err
}

func listProposals(handler *Handler, request *http.Request) (any, int, error) {
	query := request.URL.Query()
	if err := requireKnownQuery(query, queryDecision, queryKind, queryGroup, queryCurrentOnly, querySource); err != nil {
		return nil, 0, err
	}
	var groupID int64
	if rawGroup := query.Get(queryGroup); rawGroup != "" {
		parsedGroup, err := strconv.ParseInt(rawGroup, 10, 64)
		if err != nil || parsedGroup <= 0 {
			return nil, 0, newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebPositiveInteger, queryGroup))
		}
		groupID = parsedGroup
	}
	currentOnly, err := optionalBool(query.Get(queryCurrentOnly))
	if err != nil {
		return nil, 0, err
	}
	filter := app.NewProposalFilter(query.Get(queryDecision), query.Get(queryKind), groupID, currentOnly, query.Get(querySource))
	proposals, err := handler.service.ListProposals(request.Context(), filter)
	if proposals == nil {
		proposals = []app.Proposal{}
	}
	return proposals, http.StatusOK, err
}

func optionalBool(raw string) (bool, error) {
	if raw == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return false, newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebBoolean, queryCurrentOnly))
	}
	return parsed, nil
}

// listLinks : le Service valide l'état et borne la limite ; seul le format des nombres est contrôlé ici.
func listLinks(handler *Handler, request *http.Request) (any, int, error) {
	query := request.URL.Query()
	if err := requireKnownQuery(query, queryState, queryLimit, queryOffset); err != nil {
		return nil, 0, err
	}
	filter := app.LinkFilter{State: query.Get(queryState)}
	var err error
	if filter.Limit, err = optionalCount(query, queryLimit); err != nil {
		return nil, 0, err
	}
	if filter.Offset, err = optionalCount(query, queryOffset); err != nil {
		return nil, 0, err
	}
	page, err := handler.service.ListLinks(request.Context(), filter)
	if page.Items == nil {
		page.Items = []app.LinkItem{}
	}
	return page, http.StatusOK, err
}

func optionalCount(query url.Values, name string) (int, error) {
	raw := query.Get(name)
	if raw == "" {
		return 0, nil
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 0 {
		return 0, newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebNonNegativeInteger, name))
	}
	return count, nil
}

// decisionResult est la réponse d'une décision simple (mêmes champs que la sortie JSON de la CLI).
type decisionResult struct {
	ProposalID int64  `json:"proposal_id"`
	Action     string `json:"action"`
}

func acceptProposal(handler *Handler, request *http.Request) (any, int, error) {
	proposalID, err := pathID(request, "id")
	if err != nil {
		return nil, 0, err
	}
	var body struct {
		AllowPartialPack bool `json:"allow_partial_pack"`
	}
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	var options []app.DecisionOption
	if body.AllowPartialPack {
		options = append(options, app.AllowPartialPack)
	}
	err = handler.service.Accept(request.Context(), proposalID, options...)
	return decisionResult{ProposalID: proposalID, Action: "accepted"}, http.StatusOK, err
}

func rejectProposal(handler *Handler, request *http.Request) (any, int, error) {
	proposalID, err := pathID(request, "id")
	if err != nil {
		return nil, 0, err
	}
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	err = handler.service.Reject(request.Context(), proposalID)
	return decisionResult{ProposalID: proposalID, Action: "rejected"}, http.StatusOK, err
}

// chooseCandidate : candidate_id doit être le candidat de la proposition ou un membre de son litige ; la proposition
// full qui porte ce candidat devient la principale du groupe.
func chooseCandidate(handler *Handler, request *http.Request) (any, int, error) {
	proposalID, err := pathID(request, "id")
	if err != nil {
		return nil, 0, err
	}
	var body struct {
		CandidateID int64 `json:"candidate_id"`
	}
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if body.CandidateID <= 0 {
		return nil, 0, newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebPositiveInteger, "candidate_id"))
	}
	chosenID, err := handler.proposalOfCandidate(request.Context(), proposalID, body.CandidateID)
	if err != nil {
		return nil, 0, err
	}
	err = handler.service.Choose(request.Context(), chosenID)
	return decisionResult{ProposalID: chosenID, Action: "chosen"}, http.StatusOK, err
}

func (handler *Handler) proposalOfCandidate(ctx context.Context, proposalID, candidateID int64) (int64, error) {
	proposals, err := handler.service.ListProposals(ctx, app.ProposalFilter{})
	if err != nil {
		return 0, err
	}
	index := slices.IndexFunc(proposals, func(proposal app.Proposal) bool { return proposal.ID == proposalID })
	if index < 0 {
		return 0, newAPIError(http.StatusNotFound, errorCodeNotFound, i18n.Message{Key: i18n.KeyErrorProposalNotFound})
	}
	proposal := proposals[index]
	if proposal.CandidateID == candidateID {
		return proposal.ID, nil
	}
	for _, member := range proposal.Details.Candidates {
		if member.CandidateID == candidateID && member.ProposalID > 0 {
			return member.ProposalID, nil
		}
	}
	return 0, newAPIError(http.StatusUnprocessableEntity, app.ErrorCodeRefused, i18n.Message{Key: i18n.KeyErrorWebCandidateNotInGroup})
}

// forceProposal exige confirm comme la CLI exige --yes : le client réécrit des pièces à cheval sur un média lié.
func forceProposal(handler *Handler, request *http.Request) (any, int, error) {
	proposalID, err := pathID(request, "id")
	if err != nil {
		return nil, 0, err
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if !body.Confirm {
		return nil, 0, newAPIError(http.StatusBadRequest, errorCodeConfirmationRequired,
			i18n.Message{Key: i18n.KeyErrorWebConfirmRequired})
	}
	result, err := handler.service.Force(request.Context(), proposalID)
	return result, http.StatusOK, err
}

// groupResult est la réponse d'une acceptation de groupe (mêmes champs que la CLI).
type groupResult struct {
	GroupID  int64   `json:"group_id"`
	Accepted []int64 `json:"accepted"`
}

func acceptGroup(handler *Handler, request *http.Request) (any, int, error) {
	groupID, err := pathID(request, "id")
	if err != nil {
		return nil, 0, err
	}
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	accepted, err := handler.service.AcceptGroup(request.Context(), groupID)
	if accepted == nil {
		accepted = []int64{}
	}
	return groupResult{GroupID: groupID, Accepted: accepted}, http.StatusOK, err
}

func runStage(handler *Handler, request *http.Request) (any, int, error) {
	stageName := request.PathValue("stage")
	for _, stage := range app.Stages() {
		if string(stage) != stageName {
			continue
		}
		if err := decodeBody(request, &struct{}{}); err != nil {
			return nil, 0, err
		}
		return handler.startJob("run", func(ctx context.Context) (any, error) { return handler.service.RunStage(ctx, stage) })
	}
	return nil, 0, newAPIError(http.StatusBadRequest, errorCodeUsage, i18n.Message{Key: i18n.KeyErrorWebUnknownStage, Params: map[string]string{"stage": strconv.Quote(stageName)}})
}

func planDedupe(handler *Handler, request *http.Request) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	return handler.startJob("dedupe plan", func(ctx context.Context) (any, error) {
		proposals, err := handler.service.DedupePlan(ctx)
		if proposals == nil {
			proposals = []app.DedupeProposal{}
		}
		return proposals, err
	})
}

// applyBody porte les options d'une application dedupe ou repair (mêmes options que la CLI).
type applyBody struct {
	DryRun        bool `json:"dry_run"`
	AcceptPending bool `json:"accept_pending"`
}

func (body applyBody) options() []app.DecisionOption {
	var options []app.DecisionOption
	if body.DryRun {
		options = append(options, app.DryRun)
	}
	if body.AcceptPending {
		options = append(options, app.AcceptPending)
	}
	return options
}

// applyFunc est DedupeAccept ou RepairAccept.
type applyFunc func(ctx context.Context, proposalID int64, options ...app.DecisionOption) (app.MaintenanceResult, error)

func (handler *Handler) startApply(request *http.Request, action string, apply applyFunc) (any, int, error) {
	proposalID, err := pathID(request, "id")
	if err != nil {
		return nil, 0, err
	}
	var body applyBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	return handler.startJob(action, func(ctx context.Context) (any, error) { return apply(ctx, proposalID, body.options()...) })
}

func applyDedupe(handler *Handler, request *http.Request) (any, int, error) {
	return handler.startApply(request, "dedupe apply", handler.service.DedupeAccept)
}

func applyRepair(handler *Handler, request *http.Request) (any, int, error) {
	return handler.startApply(request, "repair apply", handler.service.RepairAccept)
}

func scanRepair(handler *Handler, request *http.Request) (any, int, error) {
	var body struct {
		Directory string `json:"directory"`
	}
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	return handler.startJob("repair scan", func(ctx context.Context) (any, error) {
		proposals, err := handler.service.RepairScan(ctx, body.Directory)
		if proposals == nil {
			proposals = []app.RepairProposal{}
		}
		return proposals, err
	})
}

// doctorRunBody choisit les contrôles du diagnostic complet ; vide = tous.
type doctorRunBody struct {
	Checks []string `json:"checks"`
}

// runDoctor est synchrone : chaque contrôle est borné par le délai par contrôle du diagnostic. C'est un POST : le
// diagnostic joint les services configurés et écrit des fichiers de sonde dans les dossiers de liens.
func runDoctor(handler *Handler, request *http.Request) (any, int, error) {
	var body doctorRunBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	report, err := handler.service.Doctor(request.Context(), app.DoctorOptions{CheckIDs: body.Checks})
	return renderedDoctor(handler, report), http.StatusOK, err
}

func runDoctorCheck(handler *Handler, request *http.Request) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	options := app.DoctorOptions{CheckIDs: []string{request.PathValue("checkId")}}
	report, err := handler.service.Doctor(request.Context(), options)
	return renderedDoctor(handler, report), http.StatusOK, err
}

func listSources(handler *Handler, request *http.Request) (any, int, error) {
	if err := requireKnownQuery(request.URL.Query()); err != nil {
		return nil, 0, err
	}
	rows, err := handler.service.ListSources(request.Context())
	if rows == nil {
		rows = []app.SourceRow{}
	}
	return rows, http.StatusOK, err
}

// testSource est synchrone comme le diagnostic : deux requêtes bornées par le délai de l'indexeur. C'est un POST : le
// test dépense le quota du jour.
func testSource(handler *Handler, request *http.Request) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	result, err := handler.service.TestSource(request.Context(), request.PathValue("name"))
	return result, http.StatusOK, err
}

// renderedDoctor rend le rapport dans la langue du service ; les codes et les états ne sont jamais traduits.
func renderedDoctor(handler *Handler, report doctor.Report) doctor.RenderedReport {
	return doctor.Render(report, handler.service.Translator())
}

func listJobs(handler *Handler, _ *http.Request) (any, int, error) {
	return handler.jobs.list(), http.StatusOK, nil
}

func getJob(handler *Handler, request *http.Request) (any, int, error) {
	jobID, err := pathID(request, "id")
	if err != nil {
		return nil, 0, err
	}
	job, found := handler.jobs.get(jobID)
	if !found {
		return nil, 0, newAPIError(http.StatusNotFound, errorCodeNotFound, i18n.Message{Key: i18n.KeyErrorWebJobNotFound})
	}
	return job, http.StatusOK, nil
}

// allowedMethods liste les méthodes de la table pour chaque chemin (en-tête Allow d'un 405) ; GET admet aussi HEAD.
func allowedMethods() map[string][]string {
	methods := map[string][]string{}
	for _, apiRoute := range apiRoutes {
		methods[apiRoute.path] = append(methods[apiRoute.path], apiRoute.method)
		if apiRoute.method == http.MethodGet {
			methods[apiRoute.path] = append(methods[apiRoute.path], http.MethodHead)
		}
	}
	return methods
}

// methodNotAllowed répond 405 sur un chemin connu appelé avec une autre méthode.
func methodNotAllowed(allowed []string) endpointFunc {
	return func(_ *Handler, _ *http.Request) (any, int, error) {
		return nil, 0, &apiError{
			status: http.StatusMethodNotAllowed, allow: strings.Join(allowed, ", "),
			code: errorCodeMethodNotAllowed, message: i18n.Message{Key: i18n.KeyErrorWebMethodNotAllowed},
		}
	}
}
