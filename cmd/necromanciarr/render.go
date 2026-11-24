package main

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"necromanciarr/internal/app"
	"necromanciarr/internal/doctor"
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

func writeStageResult(writer io.Writer, result pipeline.Result) {
	_, _ = fmt.Fprintf(writer, "%s: processed %d, skipped %d, failed %d\n", result.Stage, result.Processed, result.Skipped, result.Failed)
	counters := []struct {
		label string
		value int
	}{
		{"unidentified files", result.UnidentifiedFiles},
		{"candidates created", result.CandidatesCreated},
		{"candidates updated", result.CandidatesUpdated},
		{"candidates unchanged", result.CandidatesUnchanged},
		{"candidates stale", result.CandidatesStale},
		{"verified", result.Verified},
		{"partial annex", result.PartialAnnex},
		{"partial pack", result.PartialPack},
		{"conflicts", result.Conflicts},
		{"resumed", result.Resumed},
		{"proposals created", result.ProposalsCreated},
		{"linked", result.Linked},
		{"seeded", result.Seeded},
		{"seeded paused incomplete", result.SeededPausedIncomplete},
	}
	for _, counter := range counters {
		if counter.value != 0 {
			_, _ = fmt.Fprintf(writer, "  %s: %d\n", counter.label, counter.value)
		}
	}
	for _, reason := range slices.Sorted(maps.Keys(result.Reasons)) {
		_, _ = fmt.Fprintf(writer, "  reason %s: %d\n", reason, result.Reasons[reason])
	}
	if len(result.UnidentifiedSamples) > 0 {
		_, _ = fmt.Fprintf(writer, "  unidentified samples: %s\n", strings.Join(result.UnidentifiedSamples, ", "))
	}
}

func writeCounts[Key ~string](writer io.Writer, label string, counts map[Key]int) {
	parts := make([]string, 0, len(counts))
	for _, key := range slices.Sorted(maps.Keys(counts)) {
		parts = append(parts, fmt.Sprintf("%s %d", key, counts[key]))
	}
	if len(parts) == 0 {
		parts = append(parts, "none")
	}
	_, _ = fmt.Fprintf(writer, "%s: %s\n", label, strings.Join(parts, ", "))
}

func writeStatus(writer io.Writer, status app.Status) {
	writeCounts(writer, "candidates", status.Candidates)
	writeCounts(writer, "proposals", status.Proposals)
	writeCounts(writer, "applied steps", status.Steps)
	_, _ = fmt.Fprintf(writer, "cross-seed mode: %s\n", status.CrossSeedMode)
}

var proposalColumns = []int{6, 13, 10, 24, 6}

func writeProposals(writer io.Writer, proposals []app.Proposal) {
	if len(proposals) == 0 {
		_, _ = fmt.Fprintln(writer, "no proposal")
		return
	}
	writeRow(writer, proposalColumns, "ID", "KIND", "DECISION", "STEP", "GROUP", "NAME")
	for _, proposal := range proposals {
		group := "-"
		if proposal.GroupID != 0 {
			group = fmt.Sprint(proposal.GroupID)
		}
		step := string(proposal.Step)
		if step == "" {
			step = "-"
		}
		writeRow(writer, proposalColumns, fmt.Sprint(proposal.ID), string(proposal.Kind), string(proposal.Decision), step, group, proposalName(proposal))
	}
}

// proposalName : le torrent pour une proposition de torrent, le chemin traité pour dedupe et repair ; « (stale) »
// signale une génération périmée.
func proposalName(proposal app.Proposal) string {
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
		name = "(stale) " + name
	}
	return name
}

var dedupeColumns = []int{6, 10, 8, 10, 10, 7}

func writeDedupeProposals(writer io.Writer, proposals []app.DedupeProposal) {
	if len(proposals) == 0 {
		_, _ = fmt.Fprintln(writer, "no duplicate found")
		return
	}
	writeRow(writer, dedupeColumns, "ID", "DECISION", "LINK", "SAVES", "READS", "METHOD", "TORRENT FILE")
	var totalRead int64
	for _, proposal := range proposals {
		details := proposal.Details
		kind := details.Kind
		if !details.Actionable {
			kind = "none"
		}
		writeRow(writer, dedupeColumns, fmt.Sprint(proposal.ID), string(proposal.Decision), kind, humanBytes(details.SavedBytes),
			humanBytes(details.BytesToRead), details.Method, details.TorrentPath)
		if isOpenForApply(proposal) {
			totalRead += details.BytesToRead
		}
	}
	_, _ = fmt.Fprintf(writer, "estimated read to apply the open proposals: %s\n", humanBytes(totalRead))
}

func isOpenForApply(proposal app.DedupeProposal) bool {
	isOpen := proposal.Decision == "pending" || proposal.Decision == "accepted"
	return isOpen && proposal.Details.Actionable
}

var repairColumns = []int{6, 10, 10, 14}

func writeRepairProposals(writer io.Writer, proposals []app.RepairProposal) {
	if len(proposals) == 0 {
		_, _ = fmt.Fprintln(writer, "no broken link")
		return
	}
	writeRow(writer, repairColumns, "ID", "DECISION", "STATUS", "METHOD", "LINK")
	for _, proposal := range proposals {
		method := proposal.Details.Method
		if method == "" {
			method = "-"
		}
		writeRow(writer, repairColumns, fmt.Sprint(proposal.ID), string(proposal.Decision), proposal.Details.Status, method, proposal.Details.LinkPath)
		if proposal.Details.Reason != "" {
			_, _ = fmt.Fprintln(writer, fitText("       not actionable: "+proposal.Details.Reason, tableWidth))
		}
	}
}

var linkColumns = []int{6, 10, 8}

// writeLinks écrit les liens de la page, le motif d'un état non conforme en retrait, puis les effectifs et la suite.
func writeLinks(writer io.Writer, page app.LinkPage, offset int) {
	writeCounts(writer, "links", page.Counts)
	if len(page.Items) == 0 {
		_, _ = fmt.Fprintln(writer, "no recorded link")
		return
	}
	writeRow(writer, linkColumns, "ID", "STATE", "KIND", "LINK")
	for _, item := range page.Items {
		writeRow(writer, linkColumns, fmt.Sprint(item.ID), item.State, item.Kind, item.LinkPath)
		if item.Reason != "" {
			_, _ = fmt.Fprintln(writer, fitText("       "+item.Reason, tableWidth))
		}
	}
	_, _ = fmt.Fprintf(writer, "showing %d-%d of %d\n", offset+1, offset+len(page.Items), page.Total)
	if page.Next > 0 {
		_, _ = fmt.Fprintf(writer, "next page: --offset %d\n", page.Next)
	}
}

func writeMaintenanceResults(writer io.Writer, items []applyItem) {
	if len(items) == 0 {
		_, _ = fmt.Fprintln(writer, "no accepted proposal to apply")
		return
	}
	for _, item := range items {
		switch {
		case item.Error != nil:
			_, _ = fmt.Fprintf(writer, "proposal %d: failed (%s)\n", item.ProposalID, item.Error.Code)
		case item.DryRun:
			_, _ = fmt.Fprintf(writer, "proposal %d: dry run ok, would link to %s\n", item.ProposalID, item.LinkTarget)
		default:
			_, _ = fmt.Fprintf(writer, "proposal %d: applied, link to %s (%s read)\n", item.ProposalID, item.LinkTarget, humanBytes(item.BytesRead))
		}
	}
}

func writeDoctorReport(writer io.Writer, report doctor.Report) {
	for _, named := range report.Checks {
		_, _ = fmt.Fprintf(writer, "%s %s: %s\n", statusMarks[named.Status], named.Title, named.Detail)
		if named.Hint != "" {
			_, _ = fmt.Fprintf(writer, "    hint: %s\n", named.Hint)
		}
	}
}
