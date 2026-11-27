package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

// Options propres à chaque sous-commande ; toute autre option va à la configuration (--config-dir, --db-path...).
const (
	flagJSON             = "json"
	flagYes              = "yes"
	flagAllAccepted      = "all-accepted"
	flagDryRun           = "dry-run"
	flagAllowPartialPack = "allow-partial-pack"
	flagCurrentOnly      = "current-only"
	flagKind             = "kind"
	flagState            = "state"
	flagGroup            = "group"
	flagDirectory        = "dir"
	flagCheck            = "check"
	flagLimit            = "limit"
	flagOffset           = "offset"
	flagSource           = "source"
)

// Noms des actions du registre appelées par chaque sous-commande (parité CLI et web).
const (
	actionRun             = "run"
	actionStatus          = "status"
	actionProposalsList   = "proposals list"
	actionProposalsAccept = "proposals accept"
	actionProposalsGroup  = "proposals accept-group"
	actionProposalsReject = "proposals reject"
	actionProposalsChoose = "proposals choose"
	actionProposalsForce  = "proposals force"
	actionDedupePlan      = "dedupe plan"
	actionDedupeApply     = "dedupe apply"
	actionRepairScan      = "repair scan"
	actionRepairApply     = "repair apply"
	actionDoctor          = "doctor"
	actionLinksList       = "links list"
	actionSourcesList     = "sources list"
	actionSourcesTest     = "sources test"
	actionVolumesList     = "volumes list"
	actionVolumesProbe    = "volumes probe"
	actionVolumesCommands = "volumes commands"
	exampleProposalID     = "1"
)

// command est une sous-commande : path sa suite de mots, actions les actions du registre qu'elle appelle, arguments
// ses arguments positionnels (avant les options), boolFlags et valueFlags ses options, example un appel valide
// (utilisé par le test de parité). diagnostic ouvre le Service sans base ni client ; onConfigError rend un résultat
// au lieu d'échouer quand la configuration est invalide.
type command struct {
	path                []string
	summaryKey          i18n.Key
	summaryParams       map[string]string
	actions             []string
	arguments           []string
	optionalArguments   bool
	boolFlags           []string
	valueFlags          []string
	example             []string
	diagnostic          bool
	onConfigError       func(i18n.Translator, error) output
	repairsStoredConfig bool
	execute             func(ctx context.Context, call *invocation) (output, error)
}

func (spec command) name() string { return strings.Join(spec.path, " ") }

func (spec command) summary(translator i18n.Translator) string {
	return translator.Text(spec.summaryKey, spec.summaryParams)
}

var commands = buildCommands()

func buildCommands() []command {
	var built []command
	for _, stage := range app.Stages() {
		built = append(built, stageCommand(stage))
	}
	return append(append(built, []command{
		{path: []string{"status"}, summaryKey: i18n.KeyCLISummaryStatus, actions: []string{actionStatus}, execute: runStatus},
		{
			path: []string{"proposals", "list"}, summaryKey: i18n.KeyCLISummaryProposalsList,
			actions: []string{actionProposalsList}, boolFlags: []string{flagCurrentOnly}, valueFlags: []string{flagKind, flagState, flagGroup, flagSource},
			execute: runProposalsList,
		},
		{
			path: []string{"proposals", "accept"}, summaryKey: i18n.KeyCLISummaryAccept, actions: []string{actionProposalsAccept},
			arguments: []string{app.ArgumentProposalID}, boolFlags: []string{flagAllowPartialPack}, execute: runAccept,
		},
		{
			path: []string{"proposals", "accept-group"}, summaryKey: i18n.KeyCLISummaryAcceptGroup, actions: []string{actionProposalsGroup},
			arguments: []string{app.ArgumentGroupID}, execute: runAcceptGroup,
		},
		decisionCommand("reject", i18n.KeyCLISummaryReject, actionProposalsReject, func(ctx context.Context, service app.Service, proposalID int64) error {
			return service.Reject(ctx, proposalID)
		}),
		decisionCommand("choose", i18n.KeyCLISummaryChoose, actionProposalsChoose, func(ctx context.Context, service app.Service, proposalID int64) error {
			return service.Choose(ctx, proposalID)
		}),
		{
			path: []string{"proposals", "force"}, summaryKey: i18n.KeyCLISummaryForce, actions: []string{actionProposalsForce},
			arguments: []string{app.ArgumentProposalID}, boolFlags: []string{flagYes}, example: []string{exampleProposalID, "--" + flagYes},
			execute: runForce,
		},
		{path: []string{"dedupe", "plan"}, summaryKey: i18n.KeyCLISummaryDedupePlan, actions: []string{actionDedupePlan}, execute: runDedupePlan},
		{
			path: []string{"dedupe", "apply"}, summaryKey: i18n.KeyCLISummaryApply,
			actions: []string{actionDedupeApply}, arguments: []string{app.ArgumentProposalID}, optionalArguments: true,
			boolFlags: []string{flagAllAccepted, flagDryRun}, execute: maintenanceApply(store.ProposalDedupe, app.Service.DedupeAccept),
		},
		{
			path: []string{"repair", "scan"}, summaryKey: i18n.KeyCLISummaryRepairScan, actions: []string{actionRepairScan},
			valueFlags: []string{flagDirectory}, execute: runRepairScan,
		},
		{
			path: []string{"repair", "apply"}, summaryKey: i18n.KeyCLISummaryApply,
			actions: []string{actionRepairApply}, arguments: []string{app.ArgumentProposalID}, optionalArguments: true,
			boolFlags: []string{flagAllAccepted, flagDryRun}, execute: maintenanceApply(store.ProposalRepair, app.Service.RepairAccept),
		},
		{
			path: []string{"links", "list"}, summaryKey: i18n.KeyCLISummaryLinksList,
			actions: []string{actionLinksList}, valueFlags: []string{flagState, flagLimit, flagOffset}, execute: runLinksList,
		},
		{
			path: []string{"sources", "list"}, summaryKey: i18n.KeyCLISummarySourcesList, actions: []string{actionSourcesList},
			execute: runSourcesList,
		},
		{
			path: []string{"sources", "test"}, summaryKey: i18n.KeyCLISummarySourcesTest, actions: []string{actionSourcesTest},
			arguments: []string{app.ArgumentSourceName}, execute: runSourcesTest,
		},
		{
			path: []string{"volumes", "list"}, summaryKey: i18n.KeyCLISummaryVolumesList, actions: []string{actionVolumesList},
			execute: runVolumesList,
		},
		{
			path: []string{"volumes", "probe"}, summaryKey: i18n.KeyCLISummaryVolumesProbe, actions: []string{actionVolumesProbe},
			arguments: []string{app.ArgumentVolumeName}, execute: runVolumesProbe,
		},
		{
			path: []string{"volumes", "commands"}, summaryKey: i18n.KeyCLISummaryVolumesCommands, actions: []string{actionVolumesCommands},
			arguments: []string{app.ArgumentVolumeName}, execute: runVolumesCommands,
		},
		{
			path: []string{"doctor"}, summaryKey: i18n.KeyCLISummaryDoctor, actions: []string{actionDoctor},
			valueFlags: []string{flagCheck}, diagnostic: true, onConfigError: doctorConfigFailure, execute: runDoctor,
		},
	}...), configCommands()...)
}

func init() {
	for index := range commands {
		commands[index].boolFlags = append(commands[index].boolFlags, flagJSON)
		if commands[index].example == nil && len(commands[index].arguments) > 0 {
			commands[index].example = []string{exampleProposalID}
		}
	}
}

func stageCommand(stage pipeline.Stage) command {
	return command{
		path: []string{string(stage)}, summaryKey: i18n.KeyCLISummaryStage, summaryParams: map[string]string{"stage": string(stage)}, actions: []string{actionRun},
		execute: func(ctx context.Context, call *invocation) (output, error) {
			result, err := call.service.RunStage(ctx, stage)
			if err != nil {
				return output{}, err
			}
			return output{jsonValue: result, text: func(writer io.Writer) { writeStageResult(writer, call.translator, result) }, warnings: stageWarnings(call.translator, result)}, nil
		},
	}
}

func stageWarnings(translator i18n.Translator, result pipeline.Result) []string {
	warnings := warningTexts(result.WarningViews)
	if result.Failed > 0 {
		warnings = append(warnings, translator.Count(i18n.KeyCLIStageFailedWarningOne, i18n.KeyCLIStageFailedWarningMany, result.Failed, nil))
	}
	return warnings
}

func runStatus(ctx context.Context, call *invocation) (output, error) {
	status, err := call.service.Status(ctx)
	if err != nil {
		return output{}, err
	}
	return output{jsonValue: status, text: func(writer io.Writer) { writeStatus(writer, call.translator, status) }, warnings: warningTexts(status.Warnings)}, nil
}

// knownKinds et knownDecisions valident --kind et --state : une faute de frappe ne doit pas donner une liste vide.
var (
	knownKinds = []store.ProposalKind{
		store.ProposalFull, store.ProposalPartialAnnex, store.ProposalPartialPack, store.ProposalCrossSeed,
		store.ProposalConflict, store.ProposalDedupe, store.ProposalRepair,
	}
	knownDecisions = []store.ProposalDecision{
		store.ProposalPending, store.ProposalAccepted, store.ProposalRejected, store.ProposalApplied,
		store.ProposalFailed, store.ProposalSuperseded,
	}
)

func proposalFilter(call *invocation) (app.ProposalFilter, error) {
	filter := app.ProposalFilter{
		Kind: store.ProposalKind(call.value(flagKind)), Decision: store.ProposalDecision(call.value(flagState)),
		CurrentOnly: call.has(flagCurrentOnly), Source: call.value(flagSource),
	}
	if filter.Kind != "" && !slices.Contains(knownKinds, filter.Kind) {
		return filter, fmt.Errorf("%w: unknown kind %q (valid: %v)", config.ErrUsage, filter.Kind, knownKinds)
	}
	if filter.Decision != "" && !slices.Contains(knownDecisions, filter.Decision) {
		return filter, fmt.Errorf("%w: unknown state %q (valid: %v)", config.ErrUsage, filter.Decision, knownDecisions)
	}
	if call.has(flagGroup) {
		groupID, err := parseID(call.value(flagGroup))
		if err != nil {
			return filter, err
		}
		filter.GroupID = groupID
	}
	return filter, nil
}

func runProposalsList(ctx context.Context, call *invocation) (output, error) {
	filter, err := proposalFilter(call)
	if err != nil {
		return output{}, err
	}
	proposals, err := call.service.ListProposals(ctx, filter)
	if err != nil {
		return output{}, err
	}
	proposals = nonNil(proposals)
	return output{jsonValue: proposals, text: func(writer io.Writer) { writeProposals(writer, call.translator, proposals) }}, nil
}

// decisionResult est la sortie JSON d'une décision sur une proposition.
type decisionResult struct {
	ProposalID int64  `json:"proposal_id"`
	Action     string `json:"action"`
}

func decided(translator i18n.Translator, proposalID int64, action string) output {
	result := decisionResult{ProposalID: proposalID, Action: action}
	return output{jsonValue: result, text: func(writer io.Writer) {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIDecided, textParams("id", strconv.FormatInt(proposalID, 10), "action", action)))
	}}
}

func runAccept(ctx context.Context, call *invocation) (output, error) {
	proposalID, err := call.proposalID()
	if err != nil {
		return output{}, err
	}
	var options []app.DecisionOption
	if call.has(flagAllowPartialPack) {
		options = append(options, app.AllowPartialPack)
	}
	if err := call.service.Accept(ctx, proposalID, options...); err != nil {
		return output{}, err
	}
	return decided(call.translator, proposalID, actionProposalsAccept), nil
}

func decisionCommand(verb string, summaryKey i18n.Key, action string, decide func(ctx context.Context, service app.Service, proposalID int64) error) command {
	return command{
		path: []string{"proposals", verb}, summaryKey: summaryKey, actions: []string{action}, arguments: []string{app.ArgumentProposalID},
		execute: func(ctx context.Context, call *invocation) (output, error) {
			proposalID, err := call.proposalID()
			if err != nil {
				return output{}, err
			}
			if err := decide(ctx, call.service, proposalID); err != nil {
				return output{}, err
			}
			return decided(call.translator, proposalID, action), nil
		},
	}
}

// groupResult est la sortie JSON de proposals accept-group.
type groupResult struct {
	GroupID  int64   `json:"group_id"`
	Accepted []int64 `json:"accepted"`
}

func runAcceptGroup(ctx context.Context, call *invocation) (output, error) {
	groupID, err := parseID(call.arguments[app.ArgumentGroupID])
	if err != nil {
		return output{}, err
	}
	accepted, err := call.service.AcceptGroup(ctx, groupID)
	if err != nil {
		return output{}, err
	}
	result := groupResult{GroupID: groupID, Accepted: nonNil(accepted)}
	return output{jsonValue: result, text: func(writer io.Writer) {
		_, _ = fmt.Fprintln(writer, call.translator.Text(i18n.KeyCLIGroupAccepted, textParams(
			"id", strconv.FormatInt(groupID, 10), "ids", joinIDs(result.Accepted))))
	}}, nil
}

// runForce exige --yes : sans lui, l'avertissement est affiché et rien n'est démarré (code 2).
func runForce(ctx context.Context, call *invocation) (output, error) {
	proposalID, err := call.proposalID()
	if err != nil {
		return output{}, err
	}
	_, _ = fmt.Fprintln(call.stderr, "warning:", call.translator.Text(i18n.KeyCLIForceWarning, nil))
	if !call.has(flagYes) {
		return output{}, errConfirmationRequired
	}
	result, err := call.service.Force(ctx, proposalID)
	if err != nil {
		return output{}, err
	}
	return output{jsonValue: result, warnings: warningTexts(result.Warnings), text: func(writer io.Writer) {
		for _, replaced := range result.Replaced {
			_, _ = fmt.Fprintln(writer, call.translator.Text(i18n.KeyCLIForceReplaced, textParams("kind", replaced.Kind, "path", replaced.LinkPath)))
		}
		_, _ = fmt.Fprintln(writer, call.translator.Text(i18n.KeyCLIForceDone, textParams("id", strconv.FormatInt(proposalID, 10))))
	}}, nil
}

func runDedupePlan(ctx context.Context, call *invocation) (output, error) {
	proposals, err := call.service.DedupePlan(ctx)
	if err != nil {
		return output{}, err
	}
	proposals = nonNil(proposals)
	return output{jsonValue: proposals, text: func(writer io.Writer) { writeDedupeProposals(writer, call.translator, proposals) }}, nil
}

func maintenanceOptions(call *invocation) []app.DecisionOption {
	if call.has(flagDryRun) {
		return []app.DecisionOption{app.DryRun}
	}
	return nil
}

// applyMethod est DedupeAccept ou RepairAccept, appelée sur le Service de l'invocation.
type applyMethod func(service app.Service, ctx context.Context, proposalID int64, options ...app.DecisionOption) (app.MaintenanceResult, error)

// maintenanceApply : avec un id, le Service contrôle la nature et la décision puis accepte (si en attente) et applique
// cette proposition, sortie objet ; avec --all-accepted, il applique une à une les seules propositions déjà acceptées
// de la nature, sortie tableau, sans s'arrêter à un échec.
func maintenanceApply(kind store.ProposalKind, apply applyMethod) func(ctx context.Context, call *invocation) (output, error) {
	return func(ctx context.Context, call *invocation) (output, error) {
		_, hasID := call.arguments[app.ArgumentProposalID]
		if hasID == call.has(flagAllAccepted) {
			return output{}, fmt.Errorf("%w: give either a proposal id or --%s", config.ErrUsage, flagAllAccepted)
		}
		if !hasID {
			return applyAllAccepted(ctx, call, kind, apply)
		}
		proposalID, err := call.proposalID()
		if err != nil {
			return output{}, err
		}
		result, err := apply(call.service, ctx, proposalID, append(maintenanceOptions(call), app.AcceptPending)...)
		if err != nil {
			return output{}, err
		}
		items := []applyItem{{MaintenanceResult: result}}
		return output{
			jsonValue: result, warnings: itemWarnings(call.translator, items),
			text: func(writer io.Writer) { writeMaintenanceResults(writer, call.translator, items) },
		}, nil
	}
}

// applyItem est une application de --all-accepted : son résultat, ou son erreur.
type applyItem struct {
	app.MaintenanceResult
	Error *errorBody `json:"error,omitempty"`
}

// applyAllAccepted : code de sortie 3 si une proposition est périmée, sinon 1 si une a échoué, sinon 0.
func applyAllAccepted(ctx context.Context, call *invocation, kind store.ProposalKind, apply applyMethod) (output, error) {
	accepted, err := call.service.ListProposals(ctx, app.ProposalFilter{Kind: kind, Decision: store.ProposalAccepted})
	if err != nil {
		return output{}, err
	}
	items := []applyItem{}
	exitCode := successExitCode
	var failures []string
	for _, proposal := range accepted {
		result, err := apply(call.service, ctx, proposal.ID, maintenanceOptions(call)...)
		item := applyItem{MaintenanceResult: result}
		if err != nil {
			class := classify(err)
			item.ProposalID = proposal.ID
			item.Error = &errorBody{Code: class.code, Message: errorMessage(call.translator, err)}
			failures = append(failures, call.translator.Text(i18n.KeyCLIProposalLine, textParams("id", strconv.FormatInt(proposal.ID, 10), "text", item.Error.Message)))
			exitCode = worseExitCode(exitCode, class.exitCode)
		}
		items = append(items, item)
	}
	return output{
		jsonValue: items, warnings: itemWarnings(call.translator, items), failures: failures, exitCode: exitCode,
		text: func(writer io.Writer) { writeMaintenanceResults(writer, call.translator, items) },
	}, nil
}

// worseExitCode : une proposition périmée l'emporte (3) sur tout autre échec (1).
func worseExitCode(current, candidate int) int {
	if current == staleExitCode || candidate == staleExitCode {
		return staleExitCode
	}
	return failureExitCode
}

// warningTexts garde le texte rendu de chaque avertissement.
func warningTexts(warnings []i18n.Rendered) []string {
	texts := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		texts = append(texts, warning.Text)
	}
	return texts
}

func itemWarnings(translator i18n.Translator, items []applyItem) []string {
	var warnings []string
	for _, item := range items {
		for _, warning := range item.Warnings {
			warnings = append(warnings, translator.Text(i18n.KeyCLIProposalLine, textParams("id", strconv.FormatInt(item.ProposalID, 10), "text", warning.Text)))
		}
	}
	return warnings
}

func runRepairScan(ctx context.Context, call *invocation) (output, error) {
	proposals, err := call.service.RepairScan(ctx, call.value(flagDirectory))
	if err != nil {
		return output{}, err
	}
	proposals = nonNil(proposals)
	return output{jsonValue: proposals, text: func(writer io.Writer) { writeRepairProposals(writer, call.translator, proposals) }}, nil
}

func runLinksList(ctx context.Context, call *invocation) (output, error) {
	filter := app.LinkFilter{State: call.value(flagState)}
	var err error
	if filter.Limit, err = optionalCount(call, flagLimit); err != nil {
		return output{}, err
	}
	if filter.Offset, err = optionalCount(call, flagOffset); err != nil {
		return output{}, err
	}
	page, err := call.service.ListLinks(ctx, filter)
	if err != nil {
		return output{}, err
	}
	page.Items = nonNil(page.Items)
	var warnings []string
	if page.Truncated {
		warnings = []string{call.translator.Text(i18n.KeyCLILinksTruncated, nil)}
	}
	return output{jsonValue: page, warnings: warnings, text: func(writer io.Writer) { writeLinks(writer, call.translator, page, filter.Offset) }}, nil
}

func runSourcesList(ctx context.Context, call *invocation) (output, error) {
	rows, err := call.service.ListSources(ctx)
	if err != nil {
		return output{}, err
	}
	rows = nonNil(rows)
	return output{jsonValue: rows, text: func(writer io.Writer) { writeSources(writer, call.translator, rows) }}, nil
}

// runSourcesTest : un test en échec n'est pas une erreur de la commande, mais sort avec le code 1.
func runSourcesTest(ctx context.Context, call *invocation) (output, error) {
	result, err := call.service.TestSource(ctx, call.arguments[app.ArgumentSourceName])
	if err != nil {
		return output{}, err
	}
	exitCode := successExitCode
	if result.Result != app.SourceTestOK {
		exitCode = failureExitCode
	}
	return output{jsonValue: result, exitCode: exitCode, text: func(writer io.Writer) { writeSourceTest(writer, call.translator, result) }}, nil
}

func runVolumesList(ctx context.Context, call *invocation) (output, error) {
	rows, err := call.service.ListVolumes(ctx)
	if err != nil {
		return output{}, err
	}
	rows = nonNil(rows)
	return output{jsonValue: rows, text: func(writer io.Writer) { writeVolumes(writer, call.translator, rows) }}, nil
}

// runVolumesProbe : une sonde en échec n'est pas une erreur de la commande, mais sort avec le code 1.
func runVolumesProbe(ctx context.Context, call *invocation) (output, error) {
	result, err := call.service.ProbeVolume(ctx, call.arguments[app.ArgumentVolumeName])
	if err != nil {
		return output{}, err
	}
	exitCode := successExitCode
	if result.Result != app.VolumeProbeOK {
		exitCode = failureExitCode
	}
	return output{jsonValue: result, exitCode: exitCode, text: func(writer io.Writer) { writeVolumeProbe(writer, call.translator, result) }}, nil
}

// runVolumesCommands écrit les lignes seules sur stdout, telles quelles (tabulations comprises) pour un copier-coller
// dans une garde ; l'indication du script part sur stderr.
func runVolumesCommands(ctx context.Context, call *invocation) (output, error) {
	commands, err := call.service.VolumeCommands(ctx, call.arguments[app.ArgumentVolumeName])
	if err != nil {
		return output{}, err
	}
	if !call.has(flagJSON) {
		_, _ = fmt.Fprintln(call.stderr, call.translator.Text(i18n.KeyCLIVolumeCommandsHint, textParams("script", commands.GuardScript)))
	}
	return output{jsonValue: commands, text: func(writer io.Writer) {
		for _, line := range commands.Lines {
			_, _ = fmt.Fprintln(writer, line)
		}
	}}, nil
}

// optionalCount lit un entier positif ou nul ; absent vaut 0 (le Service applique alors sa valeur par défaut).
func optionalCount(call *invocation, flag string) (int, error) {
	if !call.has(flag) {
		return 0, nil
	}
	count, err := strconv.Atoi(call.value(flag))
	if err != nil || count < 0 {
		return 0, fmt.Errorf("%w: --%s must be a non-negative integer", config.ErrUsage, flag)
	}
	return count, nil
}

func runDoctor(ctx context.Context, call *invocation) (output, error) {
	report, err := call.service.Doctor(ctx, app.DoctorOptions{CheckIDs: call.flags[flagCheck]})
	if err != nil {
		return output{}, err
	}
	return doctorOutput(call.translator, report), nil
}

func doctorOutput(translator i18n.Translator, report doctor.Report) output {
	rendered := doctor.Render(report, translator)
	return output{jsonValue: rendered, exitCode: doctor.ExitCode(report), text: func(writer io.Writer) { writeDoctorReport(writer, translator, rendered) }}
}

// doctorConfigFailure remplace tous les contrôles par un seul : sans configuration valide, les autres n'ont pas de sens.
func doctorConfigFailure(translator i18n.Translator, loadErr error) output {
	check := doctor.Check{ID: doctor.IDConfig, TitleKey: i18n.KeyDoctorTitleConfig, Run: func(context.Context) doctor.Result {
		return doctor.Result{
			Status: doctor.Fail, Code: doctor.CodeConfigInvalid,
			Detail: i18n.Message{Key: i18n.KeyDoctorConfigLoadFailed, Params: map[string]string{"error": loadErr.Error()}},
			Hint:   i18n.Message{Key: i18n.KeyDoctorConfigLoadFailedHint},
		}
	}}
	checks := []doctor.Check{check}
	if secretKeyCheck, isSecretKeyFailure := doctor.SecretKeyFailure(loadErr); isSecretKeyFailure {
		checks = append(checks, secretKeyCheck)
	}
	return doctorOutput(translator, doctor.Run(context.Background(), checks))
}

func nonNil[Element any](values []Element) []Element {
	if values == nil {
		return []Element{}
	}
	return values
}

func joinIDs(identifiers []int64) string {
	texts := make([]string, 0, len(identifiers))
	for _, identifier := range identifiers {
		texts = append(texts, fmt.Sprint(identifier))
	}
	return strings.Join(texts, ", ")
}

// usageText est construit depuis la table de commande : une ligne par commande, moins de 100 colonnes.
func usageText(translator i18n.Translator) string {
	var usage strings.Builder
	usage.WriteString(translator.Text(i18n.KeyCLIUsageHeader, nil) + "\n")
	for _, spec := range commands {
		_, _ = fmt.Fprintf(&usage, "  %-23s %s\n", spec.name(), spec.summary(translator))
	}
	for _, line := range []struct {
		name string
		key  i18n.Key
	}{
		{"serve", i18n.KeyCLIUsageServe},
		{"healthcheck", i18n.KeyCLIUsageHealthcheck},
		{"api-key [--secret]", i18n.KeyCLIUsageAPIKey},
		{"config check", i18n.KeyCLIUsageConfigCheck},
		{"sftp trust <name> [--yes]", i18n.KeyCLIUsageSFTPTrust},
		{"account status", i18n.KeyCLIUsageAccountStatus},
		{"account reset [--yes]", i18n.KeyCLIUsageAccountReset},
	} {
		_, _ = fmt.Fprintf(&usage, "  %-23s %s\n", line.name, translator.Text(line.key, nil))
	}
	usage.WriteString("\n" + translator.Text(i18n.KeyCLIUsageExitCodes, nil) + "\n" + translator.Text(i18n.KeyCLIUsageAllAccepted, nil) + "\n")
	return usage.String()
}
