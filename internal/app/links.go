package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
	"necromanciarr/internal/repair"
	"necromanciarr/internal/store"
)

// États courants d'un lien enregistré, tels que ListLinks les rend.
const (
	LinkStateOK         = "ok"
	LinkStateDiverged   = "diverged"
	LinkStateLost       = "lost"
	LinkStateUnreadable = "unreadable"
)

// Bornes de ListLinks : une page sans limite en vaut DefaultLinkLimit, jamais plus de MaxLinkLimit ; au-delà de
// linkInspectionBudget, la page rendue est partielle (Truncated) plutôt que d'attendre un montage FUSE figé.
const (
	DefaultLinkLimit     = 200
	MaxLinkLimit         = 1000
	linkInspectionBudget = 20 * time.Second
)

// Motifs stables d'un état autre que « conforme » vérifié (LinkItem.Reason, jamais traduits ; Label les traduit).
const (
	LinkReasonMissing          = "link_missing"
	LinkReasonTargetMissing    = "link_target_missing"
	LinkReasonInodeDiverged    = "link_inode_diverged"
	LinkReasonTargetRenamed    = "link_target_renamed"
	LinkReasonNotCompared      = "link_not_compared"
	LinkReasonReplaced         = "link_replaced"
	LinkReasonResized          = "link_resized"
	LinkReasonTargetUnreadable = "link_target_unreadable"
	LinkReasonInspectionFailed = "link_inspection_failed"
)

// ErrInspectionBusy : un examen des liens est encore en cours (peut-être bloqué sur un montage figé) ; réessayer plus
// tard.
var ErrInspectionBusy = errors.New("app: a links inspection is still running, retry later")

// LinkStates liste les états courants qu'un filtre de ListLinks accepte.
func LinkStates() []string {
	return []string{LinkStateOK, LinkStateDiverged, LinkStateLost, LinkStateUnreadable}
}

// LinkFilter choisit les liens de ListLinks : State vide garde tous les états, Limit ≤ 0 vaut DefaultLinkLimit.
type LinkFilter struct {
	State  string
	Limit  int
	Offset int
}

// LinkItem est un lien enregistré avec son état courant ; Reason explique un état autre que « conforme » vérifié.
// Compared est faux quand rien n'a pu être comparé (reflink ou copie sans taille enregistrée) : « conforme » veut
// seulement dire que le lien existe.
type LinkItem struct {
	ID         int64     `json:"id"`
	LinkPath   string    `json:"link_path"`
	TargetPath string    `json:"target_path"`
	Kind       string    `json:"kind"`
	State      string    `json:"state"`
	RecordedAt time.Time `json:"recorded_at"`
	Size       int64     `json:"size,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	Label      string    `json:"label,omitempty"`
	Compared   bool      `json:"compared"`
	// Volume est le fournisseur sftp du lien ; vide pour le disque local.
	Volume string `json:"volume"`
}

// labelled ajoute le libellé traduit du motif.
func (item LinkItem) labelled(translator i18n.Translator) LinkItem {
	if item.Reason != "" {
		item.Label = translator.ReasonLabel(item.Reason)
	}
	return item
}

// LinkPage est une page de ListLinks : Total compte les liens du filtre, Next est l'Offset de la page suivante (absent
// à la fin), Counts compte chaque état sur tous les liens examinés. Truncated : le budget de temps est épuisé, Total
// et Counts ne portent que sur les liens examinés à temps.
type LinkPage struct {
	Items     []LinkItem     `json:"items"`
	Total     int            `json:"total"`
	Next      int            `json:"next,omitempty"`
	Truncated bool           `json:"truncated"`
	Counts    map[string]int `json:"counts"`
}

func (filter LinkFilter) normalized() (LinkFilter, error) {
	if filter.State != "" && !slices.Contains(LinkStates(), filter.State) {
		return filter, fmt.Errorf("%w: unknown link state %q (valid: %v)", config.ErrUsage, filter.State, LinkStates())
	}
	if filter.Offset < 0 {
		return filter, fmt.Errorf("%w: offset must not be negative", config.ErrUsage)
	}
	if filter.Limit <= 0 {
		filter.Limit = DefaultLinkLimit
	}
	filter.Limit = min(filter.Limit, MaxLinkLimit)
	return filter, nil
}

func (svc *service) ListLinks(ctx context.Context, filter LinkFilter) (LinkPage, error) {
	filter, err := filter.normalized()
	if err != nil {
		return LinkPage{}, err
	}
	if !svc.inspecting.CompareAndSwap(false, true) {
		return LinkPage{}, ErrInspectionBusy
	}
	release := func() { svc.inspecting.Store(false) }
	links, err := svc.deps.Store.ListLinks(ctx, "")
	if err != nil {
		release()
		return LinkPage{}, err
	}
	budgetCtx, cancel := context.WithTimeout(ctx, svc.linkBudget())
	defer cancel()
	items, truncated := inspectLinks(budgetCtx, links, svc.linkInspector(), release)
	if err := ctx.Err(); err != nil {
		return LinkPage{}, err
	}
	for position := range items {
		items[position] = items[position].labelled(svc.deps.Translator)
	}
	return pageOf(items, filter, truncated), nil
}

func (svc *service) linkBudget() time.Duration {
	if svc.inspectionBudget > 0 {
		return svc.inspectionBudget
	}
	return linkInspectionBudget
}

func (svc *service) linkInspector() func(store.Link) repair.Inspection {
	if svc.inspect != nil {
		return svc.inspect
	}
	return repair.InspectWith(svc.deps.Stages.Registry)
}

// inspectLinks examine les liens dans l'ordre jusqu'à la fin du contexte ; truncated dit que le temps a manqué.
// release n'est appelé qu'au vrai retour de la goroutine : un Stat bloqué par le noyau garde la garde prise, et les
// appels suivants sont refusés au lieu d'empiler d'autres goroutines bloquées.
func inspectLinks(ctx context.Context, links []store.Link, inspect func(store.Link) repair.Inspection, release func()) ([]LinkItem, bool) {
	results := make(chan LinkItem)
	go func() {
		defer release()
		defer close(results)
		for _, link := range links {
			if ctx.Err() != nil {
				return
			}
			item := linkItem(link, inspect(link))
			select {
			case results <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	items := make([]LinkItem, 0, len(links))
	for {
		select {
		case item, open := <-results:
			if !open {
				return items, len(items) < len(links)
			}
			items = append(items, item)
		case <-ctx.Done():
			return items, true
		}
	}
}

func linkItem(link store.Link, inspection repair.Inspection) LinkItem {
	state, reason := stateOf(inspection)
	return LinkItem{
		ID: link.ID, LinkPath: link.LinkPath, TargetPath: link.TargetPath, Kind: link.Kind, State: state,
		RecordedAt: link.CreatedAt, Size: link.Size, Reason: reason, Compared: inspection.Condition != repair.ConditionUnverified,
		Volume: link.Volume,
	}
}

// stateOf ramène les conditions de repair aux quatre états de la liste : un symlink cassé est perdu comme un lien
// disparu, un symlink remplacé ou une copie retaillée diverge, une cible renommée ou non comparée reste conforme avec
// son motif ; une copie de la taille enregistrée est conforme sans motif.
func stateOf(inspection repair.Inspection) (state, reason string) {
	switch inspection.Condition {
	case repair.ConditionLost:
		return LinkStateLost, LinkReasonMissing
	case repair.ConditionBroken:
		if inspection.Err != nil {
			return LinkStateUnreadable, LinkReasonTargetUnreadable
		}
		return LinkStateLost, LinkReasonTargetMissing
	case repair.ConditionDiverged:
		return LinkStateDiverged, LinkReasonInodeDiverged
	case repair.ConditionReplaced:
		return LinkStateDiverged, LinkReasonReplaced
	case repair.ConditionResized:
		return LinkStateDiverged, LinkReasonResized
	case repair.ConditionRenamedTarget:
		return LinkStateOK, LinkReasonTargetRenamed
	case repair.ConditionUnverified:
		return LinkStateOK, LinkReasonNotCompared
	case repair.ConditionUnreadable:
		return LinkStateUnreadable, LinkReasonInspectionFailed
	}
	return LinkStateOK, ""
}

func pageOf(items []LinkItem, filter LinkFilter, truncated bool) LinkPage {
	counts := map[string]int{}
	for _, state := range LinkStates() {
		counts[state] = 0
	}
	matching := make([]LinkItem, 0, len(items))
	for _, item := range items {
		counts[item.State]++
		if filter.State == "" || item.State == filter.State {
			matching = append(matching, item)
		}
	}
	start := min(filter.Offset, len(matching))
	end := min(start+filter.Limit, len(matching))
	page := LinkPage{Items: matching[start:end], Total: len(matching), Truncated: truncated, Counts: counts}
	if end < len(matching) {
		page.Next = end
	}
	return page
}
