package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"necromanciarr/internal/app"
	"necromanciarr/internal/doctor"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/pipeline"
)

// tableWidth est la largeur maximale d'une ligne de tableau ; la dernière colonne (nom, chemin) est tronquée au milieu.
// Aucune couleur n'est jamais écrite : la sortie reste lisible redirigée, sans dépendre d'un terminal ni de NO_COLOR.
const tableWidth = 100

const ellipsis = "…"

var statusMarks = map[doctor.Status]string{doctor.OK: "✓", doctor.Warn: "!", doctor.Fail: "✗", doctor.Skipped: "-"}

// fitText tronque au milieu, en caractères : le début et la fin d'un chemin sont les parties parlantes.
func fitText(text string, width int) string {
	runes := []rune(text)
	if len(runes) <= width || width < 3 {
		return text
	}
	head := (width - 1) / 2
	tail := width - 1 - head
	return string(runes[:head]) + ellipsis + string(runes[len(runes)-tail:])
}

// writeRow écrit les colonnes de largeur fixe puis la dernière, ajustée à tableWidth.
func writeRow(writer io.Writer, widths []int, cells ...string) {
	var line strings.Builder
	used := 0
	for index, width := range widths {
		_, _ = fmt.Fprintf(&line, "%-*s ", width, fitText(cells[index], width))
		used += width + 1
	}
	line.WriteString(fitText(cells[len(cells)-1], tableWidth-used))
	_, _ = fmt.Fprintln(writer, strings.TrimRight(line.String(), " "))
}

// humanBytes affiche une taille en unités binaires.
func humanBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	value, exponent := float64(size)/unit, 0
	for value >= unit && exponent < 4 {
		value /= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", value, "KMGTP"[exponent])
}

// textParams construit les paramètres d'un message à partir de paires nom, valeur.
func textParams(pairs ...string) map[string]string {
	params := make(map[string]string, len(pairs)/2)
	for position := 0; position+1 < len(pairs); position += 2 {
		params[pairs[position]] = pairs[position+1]
	}
	return params
}

func writeStageResult(writer io.Writer, translator i18n.Translator, result pipeline.Result) {
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIStageResult, textParams(
		"stage", string(result.Stage),
		"processed", translator.Count(i18n.KeyCLIStageProcessedOne, i18n.KeyCLIStageProcessedMany, result.Processed, nil),
		"skipped", translator.Count(i18n.KeyCLIStageSkippedOne, i18n.KeyCLIStageSkippedMany, result.Skipped, nil),
		"failed", translator.Text(i18n.KeyCLIStageFailedCount, textParams("count", strconv.Itoa(result.Failed))))))
	counters := []struct {
		labelKey i18n.Key
		value    int
	}{
		{i18n.KeyCLICounterUnidentified, result.UnidentifiedFiles},
		{i18n.KeyCLICounterCreated, result.CandidatesCreated},
		{i18n.KeyCLICounterUpdated, result.CandidatesUpdated},
		{i18n.KeyCLICounterUnchanged, result.CandidatesUnchanged},
		{i18n.KeyCLICounterStale, result.CandidatesStale},
		{i18n.KeyCLICounterVerified, result.Verified},
		{i18n.KeyCLICounterPartialAnnex, result.PartialAnnex},
		{i18n.KeyCLICounterPartialPack, result.PartialPack},
		{i18n.KeyCLICounterConflicts, result.Conflicts},
		{i18n.KeyCLICounterResumed, result.Resumed},
		{i18n.KeyCLICounterProposals, result.ProposalsCreated},
		{i18n.KeyCLICounterLinked, result.Linked},
		{i18n.KeyCLICounterSeeded, result.Seeded},
		{i18n.KeyCLICounterSeededPaused, result.SeededPausedIncomplete},
	}
	for _, counter := range counters {
		if counter.value != 0 {
			_, _ = fmt.Fprintln(writer, "  "+translator.Text(i18n.KeyCLICountsLine, textParams(
				"label", translator.Text(counter.labelKey, nil), "values", strconv.Itoa(counter.value))))
		}
	}
	for _, reason := range slices.Sorted(maps.Keys(result.Reasons)) {
		_, _ = fmt.Fprintln(writer, "  "+translator.Text(i18n.KeyCLIStageReason, textParams(
			"reason", reason, "count", strconv.Itoa(result.Reasons[reason]))))
	}
	if len(result.UnidentifiedSamples) > 0 {
		_, _ = fmt.Fprintln(writer, "  "+translator.Text(i18n.KeyCLIStageSamples, textParams("samples", strings.Join(result.UnidentifiedSamples, ", "))))
	}
}

func writeCounts[Key ~string](writer io.Writer, translator i18n.Translator, labelKey i18n.Key, counts map[Key]int) {
	parts := make([]string, 0, len(counts))
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}
	if len(parts) == 0 {
		parts = append(parts, translator.Text(i18n.KeyCLINone, nil))
	}
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLICountsLine, textParams(
		"label", translator.Text(labelKey, nil), "values", strings.Join(parts, ", "))))
}

func writeStatus(writer io.Writer, translator i18n.Translator, status app.Status) {
	writeCounts(writer, translator, i18n.KeyCLILabelCandidates, status.Candidates)
	writeCounts(writer, translator, i18n.KeyCLILabelProposals, status.Proposals)
	writeCounts(writer, translator, i18n.KeyCLILabelAppliedSteps, status.Steps)
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIStatusCrossSeedMode, textParams("mode", status.CrossSeedMode)))
	for _, held := range status.Held {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIStatusHeld, textParams(
			"id", strconv.FormatInt(held.CandidateID, 10), "name", held.CandidateName, "kind", string(held.Kind), "reason", held.Reason,
			"missing", strconv.Itoa(held.MissingPieces), "total", strconv.Itoa(held.TotalPieces))))
	}
}

// headerCells traduit les clés d'en-têtes de colonnes.
func headerCells(translator i18n.Translator, keys ...i18n.Key) []string {
	cells := make([]string, 0, len(keys))
	for _, key := range keys {
		cells = append(cells, translator.Text(key, nil))
	}
	return cells
}

var proposalColumns = []int{6, 13, 10, 24, 6}

func writeProposals(writer io.Writer, translator i18n.Translator, proposals []app.Proposal) {
	if len(proposals) == 0 {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIProposalsNone, nil))
		return
	}
	writeRow(writer, proposalColumns, headerCells(translator, i18n.KeyCLIHeaderID, i18n.KeyCLIHeaderKind, i18n.KeyCLIHeaderDecision,
		i18n.KeyCLIHeaderStep, i18n.KeyCLIHeaderGroup, i18n.KeyCLIHeaderName)...)
	for _, proposal := range proposals {
		group := "-"
		if proposal.GroupID != 0 {
			group = fmt.Sprint(proposal.GroupID)
		}
		step := string(proposal.Step)
		if step == "" {
			step = "-"
		}
		writeRow(writer, proposalColumns, fmt.Sprint(proposal.ID), string(proposal.Kind), string(proposal.Decision), step, group,
			proposalName(translator, proposal))
	}
}

// proposalName : le torrent pour une proposition de torrent, le chemin traité pour dedupe et repair ; le préfixe
// « (périmée) » signale une génération périmée.
func proposalName(translator i18n.Translator, proposal app.Proposal) string {
	name := proposal.CandidateName
	if proposal.Maintenance != nil {
		var paths struct {
			TorrentPath string `json:"torrent_path"`
			LinkPath    string `json:"link_path"`
		}
		_ = json.Unmarshal(proposal.Maintenance, &paths)
		name = paths.TorrentPath + paths.LinkPath
	}
	if !proposal.Current {
		name = translator.Text(i18n.KeyCLIStalePrefix, nil) + name
	}
	return name
}

var dedupeColumns = []int{6, 10, 8, 10, 10, 7}

func writeDedupeProposals(writer io.Writer, translator i18n.Translator, proposals []app.DedupeProposal) {
	if len(proposals) == 0 {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIDedupeNone, nil))
		return
	}
	writeRow(writer, dedupeColumns, headerCells(translator, i18n.KeyCLIHeaderID, i18n.KeyCLIHeaderDecision, i18n.KeyCLIHeaderLink,
		i18n.KeyCLIHeaderSaves, i18n.KeyCLIHeaderReads, i18n.KeyCLIHeaderMethod, i18n.KeyCLIHeaderTorrentFile)...)
	var totalRead int64
	for _, proposal := range proposals {
		details := proposal.Details
		kind := details.Kind
		if !details.Actionable {
			kind = translator.Text(i18n.KeyCLINone, nil)
		}
		writeRow(writer, dedupeColumns, fmt.Sprint(proposal.ID), string(proposal.Decision), kind, humanBytes(details.SavedBytes),
			humanBytes(details.BytesToRead), details.Method, details.TorrentPath)
		for _, warning := range proposal.Warnings {
			_, _ = fmt.Fprintln(writer, fitText("       warning: "+warning, tableWidth))
		}
		if isOpenForApply(proposal) {
			totalRead += details.BytesToRead
		}
	}
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIDedupeTotalRead, textParams("size", humanBytes(totalRead))))
}

func isOpenForApply(proposal app.DedupeProposal) bool {
	isOpen := proposal.Decision == "pending" || proposal.Decision == "accepted"
	return isOpen && proposal.Details.Actionable
}

var repairColumns = []int{6, 10, 10, 14}

func writeRepairProposals(writer io.Writer, translator i18n.Translator, proposals []app.RepairProposal) {
	if len(proposals) == 0 {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIRepairNone, nil))
		return
	}
	writeRow(writer, repairColumns, headerCells(translator, i18n.KeyCLIHeaderID, i18n.KeyCLIHeaderDecision, i18n.KeyCLIHeaderStatus,
		i18n.KeyCLIHeaderMethod, i18n.KeyCLIHeaderLink)...)
	for _, proposal := range proposals {
		method := proposal.Details.Method
		if method == "" {
			method = "-"
		}
		writeRow(writer, repairColumns, fmt.Sprint(proposal.ID), string(proposal.Decision), proposal.Details.Status, method, proposal.Details.LinkPath)
		if proposal.Details.Reason != "" {
			notActionable := translator.Text(i18n.KeyCLIRepairNotActionable, textParams("reason", proposal.Details.Reason))
			_, _ = fmt.Fprintln(writer, fitText("       "+notActionable, tableWidth))
		}
	}
}

var linkColumns = []int{6, 10, 8}

// writeLinks écrit les liens de la page, le motif d'un état non conforme en retrait, puis les effectifs et la suite.
func writeLinks(writer io.Writer, translator i18n.Translator, page app.LinkPage, offset int) {
	writeCounts(writer, translator, i18n.KeyCLILabelLinks, page.Counts)
	if len(page.Items) == 0 {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLILinksNone, nil))
		return
	}
	writeRow(writer, linkColumns, headerCells(translator, i18n.KeyCLIHeaderID, i18n.KeyCLIHeaderState, i18n.KeyCLIHeaderKind,
		i18n.KeyCLIHeaderLink)...)
	for _, item := range page.Items {
		writeRow(writer, linkColumns, fmt.Sprint(item.ID), item.State, item.Kind, item.LinkPath)
		if item.Reason != "" {
			_, _ = fmt.Fprintln(writer, fitText("       "+item.Reason, tableWidth))
		}
	}
	_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLILinksShowing, textParams(
		"from", strconv.Itoa(offset+1), "to", strconv.Itoa(offset+len(page.Items)), "total", strconv.Itoa(page.Total))))
	if page.Next > 0 {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLILinksNextPage, textParams("offset", strconv.Itoa(page.Next))))
	}
}

func writeMaintenanceResults(writer io.Writer, translator i18n.Translator, items []applyItem) {
	if len(items) == 0 {
		_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIMaintenanceNone, nil))
		return
	}
	for _, item := range items {
		proposalID := strconv.FormatInt(item.ProposalID, 10)
		switch {
		case item.Error != nil:
			_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIMaintenanceFailed, textParams("id", proposalID, "code", item.Error.Code)))
		case item.DryRun:
			_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIMaintenanceDryRun, textParams("id", proposalID, "target", item.LinkTarget)))
		default:
			_, _ = fmt.Fprintln(writer, translator.Text(i18n.KeyCLIMaintenanceApplied, textParams(
				"id", proposalID, "target", item.LinkTarget, "size", humanBytes(item.BytesRead))))
		}
	}
}

func writeDoctorReport(writer io.Writer, translator i18n.Translator, report doctor.RenderedReport) {
	for _, named := range report.Checks {
		_, _ = fmt.Fprintf(writer, "%s %s: %s\n", statusMarks[named.Status], named.Title, named.Detail)
		if named.Hint != "" {
			_, _ = fmt.Fprintln(writer, "    "+translator.Text(i18n.KeyCLIDoctorHint, textParams("hint", named.Hint)))
		}
	}
}
