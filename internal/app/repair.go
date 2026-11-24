package app

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"necromanciarr/internal/hashsource"
	"necromanciarr/internal/pipeline"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// États d'une proposition repair : seul un lien réparable (un seul candidat) s'accepte.
const (
	RepairStatusRepairable = "repairable"
	RepairStatusAmbiguous  = "ambiguous"
	RepairStatusLost       = "lost"
)

// RepairDetails est le détail JSON d'une proposition repair : faire pointer LinkPath vers NewTarget au lieu de
// OldTarget. Status vaut repairable, ambiguous (Candidates liste les fichiers possibles, aucun n'est choisi) ou lost
// (rien trouvé) ; seuls les repairable sont Actionable. ScanDirectory est le dossier indiqué au scan, seul dossier
// hors configuration où l'application peut remplacer un lien.
type RepairDetails struct {
	Status        string   `json:"status"`
	LinkPath      string   `json:"link_path"`
	OldTarget     string   `json:"old_target,omitempty"`
	NewTarget     string   `json:"new_target,omitempty"`
	Size          int64    `json:"size,omitempty"`
	Method        string   `json:"method,omitempty"`
	Candidates    []string `json:"candidates,omitempty"`
	Relative      bool     `json:"relative"`
	Actionable    bool     `json:"actionable"`
	ScanDirectory string   `json:"scan_directory,omitempty"`
}

// RepairProposal est une proposition repair telle que la CLI et le web l'affichent.
type RepairProposal struct {
	ID       int64                  `json:"id"`
	Decision store.ProposalDecision `json:"decision"`
	Warnings []string               `json:"warnings"`
	Details  RepairDetails          `json:"details"`
}

func (details RepairDetails) identity() (key, signature string) {
	signature = fmt.Sprint(details.Status, "\x00", details.OldTarget, "\x00", details.NewTarget, "\x00", details.Size, "\x00",
		strings.Join(details.Candidates, "\x00"))
	return details.LinkPath, signature
}

func repairActionable(details RepairDetails) bool { return details.Actionable }

func noRepairWarnings(RepairDetails) []string { return nil }

func (details RepairDetails) repair() repair.Repair {
	return repair.Repair{
		LinkPath: details.LinkPath, OldTarget: details.OldTarget, NewTarget: details.NewTarget, Size: details.Size,
		Method: repair.RepairMethod(details.Method), Candidates: details.Candidates, Relative: details.Relative,
	}
}

func (svc *service) RepairScan(ctx context.Context, directory string) ([]RepairProposal, error) {
	var proposals []RepairProposal
	err := svc.locked(ctx, lockNameRepair, func(ctx context.Context) error {
		entries, err := svc.scanRepairs(ctx, directory)
		if err != nil {
			return err
		}
		persisted, err := persistMaintenance(ctx, svc, store.ProposalRepair, entries, noRepairWarnings)
		for _, stored := range persisted {
			proposals = append(proposals, RepairProposal{
				ID: stored.proposal.ID, Decision: stored.proposal.Decision, Warnings: stored.proposal.Warnings, Details: stored.details,
			})
		}
		return err
	})
	return proposals, err
}

// scanRepairs examine les liens enregistrés puis les liens inconnus de dedupe.torrentRoots et du dossier indiqué ; un
// dossier indiqué dans la bibliothèque est refusé.
func (svc *service) scanRepairs(ctx context.Context, directory string) ([]RepairDetails, error) {
	settings := svc.deps.Config
	scanDirectories := slices.Clone(settings.Dedupe.TorrentRoots)
	if directory != "" {
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			return nil, fmt.Errorf("app: repair directory %s is not a directory", directory)
		}
		if err := checkOutsideLibrary([]string{directory}, settings.LibraryRoots, ErrDirectoryInLibrary); err != nil {
			return nil, err
		}
		scanDirectories = append(scanDirectories, directory)
	}
	library, err := svc.deps.Store.ListMediaFiles(ctx)
	if err != nil {
		return nil, err
	}
	options := repair.Options{
		LibraryRoots: settings.LibraryRoots, ScanDirectories: scanDirectories, Limiter: svc.deps.Stages.ReadLimiter,
		FingerprintCache: svc.deps.Store, HashConfirmer: hashsource.NewConfirmer(svc.deps.HashSources), Store: svc.deps.Store,
	}
	if svc.deps.Maintenance != nil {
		options.TorrentSource = clientView{client: svc.deps.Maintenance, mapper: svc.deps.Stages.ClientMapper}
	}
	repairs, report, err := repair.Scan(ctx, options, nil, library)
	if err != nil {
		return nil, err
	}
	return repairEntries(repairs, report, directory), nil
}

func repairEntries(repairs []repair.Repair, report repair.Report, directory string) []RepairDetails {
	entries := make([]RepairDetails, 0, len(repairs)+len(report.Ambiguous)+len(report.Lost))
	for _, found := range repairs {
		entries = append(entries, RepairDetails{
			Status: RepairStatusRepairable, LinkPath: found.LinkPath, OldTarget: found.OldTarget, NewTarget: found.NewTarget,
			Size: found.Size, Method: string(found.Method), Candidates: found.Candidates, Relative: found.Relative,
			Actionable: true, ScanDirectory: directory,
		})
	}
	for _, ambiguous := range report.Ambiguous {
		entries = append(entries, RepairDetails{Status: RepairStatusAmbiguous, LinkPath: ambiguous.LinkPath, Candidates: ambiguous.Candidates})
	}
	for _, lostPath := range report.Lost {
		entries = append(entries, RepairDetails{Status: RepairStatusLost, LinkPath: lostPath})
	}
	return entries
}

func (svc *service) RepairAccept(ctx context.Context, proposalID int64, options ...DecisionOption) (MaintenanceResult, error) {
	result := MaintenanceResult{ProposalID: proposalID, DryRun: slices.Contains(options, DryRun)}
	err := svc.locked(ctx, lockNameRepair, func(ctx context.Context) error {
		details, err := loadAcceptedMaintenance(ctx, svc, proposalID, store.ProposalRepair, repairActionable)
		if err != nil {
			return err
		}
		return svc.applyRepair(ctx, details, &result)
	})
	return result, err
}

// applyRepair ne remplace qu'un lien sous les dossiers de liens, les racines torrent ou le dossier du scan, jamais
// sous la bibliothèque.
func (svc *service) applyRepair(ctx context.Context, details RepairDetails, result *MaintenanceResult) error {
	settings := svc.deps.Config
	if err := checkOutsideLibrary([]string{details.LinkPath}, settings.LibraryRoots, ErrDirectoryInLibrary); err != nil {
		return err
	}
	roots := slices.Concat(settings.LinkDirs, settings.Dedupe.TorrentRoots)
	if details.ScanDirectory != "" {
		roots = append(roots, details.ScanDirectory)
	}
	applyOptions := repair.ApplyOptions{Roots: existingDirectories(roots), DryRun: result.DryRun, Store: svc.deps.Store}
	applied, applyErr := repair.Apply(ctx, []repair.Repair{details.repair()}, applyOptions)
	if len(applied.Outcomes) == 0 {
		return applyErr
	}
	outcome := applied.Outcomes[0]
	result.LinkTarget = outcome.LinkTarget
	if result.DryRun {
		return outcome.Err
	}
	if outcome.Done && outcome.Err != nil {
		result.Warnings = []string{WarningLinkNotRecorded}
	}
	return svc.finishMaintenance(ctx, result, outcome.Done, outcome.Err, repair.ErrChangedSinceScan, repair.ErrTargetInvalid)
}

// MaintenanceClient est la part du client torrent utilisée par dedupe (recontrôle) et repair (torrent d'un lien) ;
// *qbit.Client convient. Les chemins reçus sont ceux du client.
type MaintenanceClient interface {
	RecheckPath(ctx context.Context, path string) error
	TorrentForPath(ctx context.Context, path string) (repair.TorrentRef, bool, error)
}

// clientView traduit les chemins de ce processus vers la vue du client (mapper nil : mêmes chemins).
type clientView struct {
	client MaintenanceClient
	mapper pipeline.ClientPathMapper
}

func (view clientView) clientPath(path string) (string, error) {
	if view.mapper == nil {
		return path, nil
	}
	return view.mapper.Translate(path)
}

func (view clientView) RecheckPath(ctx context.Context, path string) error {
	clientPath, err := view.clientPath(path)
	if err != nil {
		return err
	}
	return view.client.RecheckPath(ctx, clientPath)
}

func (view clientView) TorrentForPath(ctx context.Context, path string) (repair.TorrentRef, bool, error) {
	clientPath, err := view.clientPath(path)
	if err != nil {
		return repair.TorrentRef{}, false, err
	}
	return view.client.TorrentForPath(ctx, clientPath)
}
