package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/store"
)

// Options propres aux sous-commandes ; toute autre option va à la configuration (--config-dir, --db-path...).
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
)

// Noms des actions du registre appelées par les sous-commandes (parité CLI et web).
const (
	actionRun                = "run"
	actionStatus             = "status"
	actionProposalsList      = "proposals list"
	actionProposalsAccept    = "proposals accept"
	actionProposalsGroup     = "proposals accept-group"
	actionProposalsReject    = "proposals reject"
	actionProposalsChoose    = "proposals choose"
	actionProposalsForce     = "proposals force"
	actionDedupePlan         = "dedupe plan"
	actionDedupeApply        = "dedupe apply"
	actionRepairScan         = "repair scan"
	actionRepairApply        = "repair apply"
	actionDoctor             = "doctor"
	exampleProposalID        = "1"
	forceStartsClientWarning = "starting a partial_annex lets the client download its missing pieces: pieces that " +
		"straddle a linked media file are written into it; the bytes are identical but the file is rewritten (an " +
		"object store such as S3 uploads the whole object again)"
)

// command est une sous-commande : path sa suite de mots, actions les actions du registre qu'elle appelle, arguments
// ses arguments positionnels (avant les options), boolFlags et valueFlags ses options, example un appel valide
// (utilisé par le test de parité). diagnostic ouvre le Service sans base ni client ; onConfigError rend un résultat
// au lieu d'échouer quand la configuration est invalide.
type command struct {
	path              []string
	summary           string
	actions           []string
	arguments         []string
	optionalArguments bool
	boolFlags         []string
	valueFlags        []string
	example           []string
	diagnostic        bool
	onConfigError     func(error) output
	execute           func(ctx context.Context, call *invocation) (output, error)
}

func (spec command) name() string { return strings.Join(spec.path, " ") }

var commands = buildCommands()

func buildCommands() []command {
	var built []command
	for _, stage := range []pipeline.Stage{pipeline.StageScan, pipeline.StageMatch, pipeline.StageVerify, pipeline.StageLink, pipeline.StageSeed} {
		built = append(built, stageCommand(stage))
	}
	return append(built, []command{
		{path: []string{"status"}, summary: "summarize candidates, proposals and the cross-seed mode", actions: []string{actionStatus}, execute: runStatus},
		{
			path: []string{"proposals", "list"}, summary: "list proposals [--kind K] [--state S] [--group G] [--current-only]",
			actions: []string{actionProposalsList}, boolFlags: []string{flagCurrentOnly}, valueFlags: []string{flagKind, flagState, flagGroup},
			execute: runProposalsList,
		},
		{
			path: []string{"proposals", "accept"}, summary: "accept a proposal <id> [--allow-partial-pack]", actions: []string{actionProposalsAccept},
			arguments: []string{app.ArgumentProposalID}, boolFlags: []string{flagAllowPartialPack}, execute: runAccept,
		},
		{
			path: []string{"proposals", "accept-group"}, summary: "accept the primary of a group <group>", actions: []string{actionProposalsGroup},
			arguments: []string{app.ArgumentGroupID}, execute: runAcceptGroup,
		},
		decisionCommand("reject", "reject a proposal <id>", actionProposalsReject, func(ctx context.Context, service app.Service, proposalID int64) error {
			return service.Reject(ctx, proposalID)
		}),
		decisionCommand("choose", "make a full proposal <id> the primary of its group", actionProposalsChoose, func(ctx context.Context, service app.Service, proposalID int64) error {
			return service.Choose(ctx, proposalID)
		}),
		{
			path: []string{"proposals", "force"}, summary: "start a paused partial_annex <id> --yes", actions: []string{actionProposalsForce},
			arguments: []string{app.ArgumentProposalID}, boolFlags: []string{flagYes}, example: []string{exampleProposalID, "--" + flagYes},
			execute: runForce,
		},
		{path: []string{"dedupe", "plan"}, summary: "propose links for torrent-side copies of library files", actions: []string{actionDedupePlan}, execute: runDedupePlan},
		{
			path: []string{"dedupe", "apply"}, summary: "accept and apply <id>, or apply --all-accepted [--dry-run]",
			actions: []string{actionProposalsAccept, actionDedupeApply}, arguments: []string{app.ArgumentProposalID}, optionalArguments: true,
			boolFlags: []string{flagAllAccepted, flagDryRun}, execute: runDedupeApply,
		},
		{
			path: []string{"repair", "scan"}, summary: "find broken links and propose their new target [--dir D]", actions: []string{actionRepairScan},
			valueFlags: []string{flagDirectory}, execute: runRepairScan,
		},
		{
			path: []string{"repair", "apply"}, summary: "accept and apply a repair proposal <id> [--dry-run]",
			actions: []string{actionProposalsAccept, actionRepairApply}, arguments: []string{app.ArgumentProposalID},
			boolFlags: []string{flagDryRun}, execute: runRepairApply,
		},
		{
			path: []string{"doctor"}, summary: "check every configured component [--check ID]...", actions: []string{actionDoctor},
			valueFlags: []string{flagCheck}, diagnostic: true, onConfigError: doctorConfigFailure, execute: runDoctor,
		},
	}...)
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
		path: []string{string(stage)}, summary: "run the " + string(stage) + " stage", actions: []string{actionRun},
		execute: func(ctx context.Context, call *invocation) (output, error) {
			result, err := call.service.RunStage(ctx, stage)
			if err != nil {
				return output{}, err
			}
			return output{jsonValue: result, text: func(writer io.Writer) { writeStageResult(writer, result) }, warnings: stageWarnings(result)}, nil
		},
	}
}

func stageWarnings(result pipeline.Result) []string {
	warnings := slices.Clone(result.Warnings)
	if result.Failed > 0 {
		warnings = append(warnings, fmt.Sprintf("%d item(s) failed, see the reasons above", result.Failed))
	}
	return warnings
}

func runStatus(ctx context.Context, call *invocation) (output, error) {
	status, err := call.service.Status(ctx)
	if err != nil {
		return output{}, err
	}
	return output{jsonValue: status, text: func(writer io.Writer) { writeStatus(writer, status) }, warnings: status.Warnings}, nil
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
		CurrentOnly: call.has(flagCurrentOnly),
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
	return output{jsonValue: proposals, text: func(writer io.Writer) { writeProposals(writer, proposals) }}, nil
}

// decisionResult est la sortie JSON d'une décision sur une proposition.
type decisionResult struct {
	ProposalID int64  `json:"proposal_id"`
	Action     string `json:"action"`
}

func decided(proposalID int64, action string) output {
	result := decisionResult{ProposalID: proposalID, Action: action}
	return output{jsonValue: result, text: func(writer io.Writer) {
		_, _ = fmt.Fprintf(writer, "proposal %d: %s done\n", proposalID, action)
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
	return decided(proposalID, actionProposalsAccept), nil
}

func decisionCommand(verb, summary, action string, decide func(ctx context.Context, service app.Service, proposalID int64) error) command {
	return command{
		path: []string{"proposals", verb}, summary: summary, actions: []string{action}, arguments: []string{app.ArgumentProposalID},
		execute: func(ctx context.Context, call *invocation) (output, error) {
			proposalID, err := call.proposalID()
			if err != nil {
				return output{}, err
			}
			if err := decide(ctx, call.service, proposalID); err != nil {
				return output{}, err
			}
			return decided(proposalID, action), nil
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
		_, _ = fmt.Fprintf(writer, "group %d: accepted proposals %s\n", groupID, joinIDs(result.Accepted))
	}}, nil
}

// runForce exige --yes : sans lui, l'avertissement est affiché et rien n'est démarré (code 2).
func runForce(ctx context.Context, call *invocation) (output, error) {
	proposalID, err := call.proposalID()
	if err != nil {
		return output{}, err
	}
	_, _ = fmt.Fprintln(call.stderr, "warning:", forceStartsClientWarning)
	if !call.has(flagYes) {
		return output{}, errConfirmationRequired
	}
	result, err := call.service.Force(ctx, proposalID)
	if err != nil {
		return output{}, err
	}
	return output{jsonValue: result, warnings: result.Warnings, text: func(writer io.Writer) {
		_, _ = fmt.Fprintf(writer, "proposal %d: forced, the client may now download its missing pieces\n", proposalID)
	}}, nil
}

func runDedupePlan(ctx context.Context, call *invocation) (output, error) {
	proposals, err := call.service.DedupePlan(ctx)
	if err != nil {
		return output{}, err
	}
	proposals = nonNil(proposals)
	return output{jsonValue: proposals, text: func(writer io.Writer) { writeDedupeProposals(writer, proposals) }}, nil
}

func maintenanceOptions(call *invocation) []app.DecisionOption {
	if call.has(flagDryRun) {
		return []app.DecisionOption{app.DryRun}
	}
	return nil
}

// runDedupeApply : avec un id, accepte puis applique cette proposition ; avec --all-accepted, applique seulement les
// propositions dedupe déjà acceptées (jamais une proposition en attente).
func runDedupeApply(ctx context.Context, call *invocation) (output, error) {
	_, hasID := call.arguments[app.ArgumentProposalID]
	if hasID == call.has(flagAllAccepted) {
		return output{}, fmt.Errorf("%w: give either a proposal id or --%s", config.ErrUsage, flagAllAccepted)
	}
	if hasID {
		return acceptAndApply(ctx, call, call.service.DedupeAccept)
	}
	accepted, err := call.service.ListProposals(ctx, app.ProposalFilter{Kind: store.ProposalDedupe, Decision: store.ProposalAccepted})
	if err != nil {
		return output{}, err
	}
	results := []app.MaintenanceResult{}
	for _, proposal := range accepted {
		result, err := call.service.DedupeAccept(ctx, proposal.ID, maintenanceOptions(call)...)
		if err != nil {
			return output{}, fmt.Errorf("proposal %d: %w", proposal.ID, err)
		}
		results = append(results, result)
	}
	return maintenanceOutput(results), nil
}

type applyFunc func(ctx context.Context, proposalID int64, options ...app.DecisionOption) (app.MaintenanceResult, error)

// acceptAndApply : un essai à blanc n'accepte rien.
func acceptAndApply(ctx context.Context, call *invocation, apply applyFunc) (output, error) {
	proposalID, err := call.proposalID()
	if err != nil {
		return output{}, err
	}
	if !call.has(flagDryRun) {
		if err := call.service.Accept(ctx, proposalID); err != nil {
			return output{}, err
		}
	}
	result, err := apply(ctx, proposalID, maintenanceOptions(call)...)
	if err != nil {
		return output{}, err
	}
	return maintenanceOutput([]app.MaintenanceResult{result}), nil
}

func maintenanceOutput(results []app.MaintenanceResult) output {
	var warnings []string
	for _, result := range results {
		for _, warning := range result.Warnings {
			warnings = append(warnings, fmt.Sprintf("proposal %d: %s", result.ProposalID, warning))
		}
	}
	var jsonValue any = results
	if len(results) == 1 {
		jsonValue = results[0]
	}
	return output{jsonValue: jsonValue, warnings: warnings, text: func(writer io.Writer) { writeMaintenanceResults(writer, results) }}
}

func runRepairScan(ctx context.Context, call *invocation) (output, error) {
	proposals, err := call.service.RepairScan(ctx, call.value(flagDirectory))
	if err != nil {
		return output{}, err
	}
	proposals = nonNil(proposals)
	return output{jsonValue: proposals, text: func(writer io.Writer) { writeRepairProposals(writer, proposals) }}, nil
}

func runRepairApply(ctx context.Context, call *invocation) (output, error) {
	return acceptAndApply(ctx, call, call.service.RepairAccept)
}

func runDoctor(ctx context.Context, call *invocation) (output, error) {
	report, err := call.service.Doctor(ctx, app.DoctorOptions{CheckIDs: call.flags[flagCheck]})
	if err != nil {
		return output{}, err
	}
	return doctorOutput(report), nil
}

func doctorOutput(report doctor.Report) output {
	return output{jsonValue: report, exitCode: doctor.ExitCode(report), text: func(writer io.Writer) { writeDoctorReport(writer, report) }}
}

// doctorConfigFailure remplace tous les contrôles par un seul : sans configuration valide, les autres n'ont pas de sens.
func doctorConfigFailure(loadErr error) output {
	check := doctor.Check{ID: doctor.IDConfig, Title: "Configuration", Run: func(context.Context) doctor.Result {
		return doctor.Result{Status: doctor.Fail, Detail: loadErr.Error(), Hint: "fix the configuration, then run `necromanciarr config check`"}
	}}
	return doctorOutput(doctor.Run(context.Background(), []doctor.Check{check}))
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

// usageText est construit depuis la table des commandes : une ligne par commande, moins de 100 colonnes.
var usageText = buildUsage()

func buildUsage() string {
	var usage strings.Builder
	usage.WriteString("usage: necromanciarr <command> [arguments] [--json] [--config-dir D ...]\n" +
		"positional arguments come before options\n\ncommands:\n")
	for _, spec := range commands {
		_, _ = fmt.Fprintf(&usage, "  %-23s %s\n", spec.name(), spec.summary)
	}
	usage.WriteString("  serve                   web interface (not available yet)\n" +
		"  api-key                 print the API key of the web interface\n" +
		"  config check            print the effective configuration, secrets masked\n" +
		"\nexit codes: 0 ok (warnings on stderr), 1 failure, 2 usage, 3 stale proposal, 4 refused\n")
	return usage.String()
}
