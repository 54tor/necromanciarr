package sources

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode"

	"necromanciarr/internal/config"
	"necromanciarr/internal/libraryprovider"
	"necromanciarr/internal/store"
	"necromanciarr/internal/torrentcheck"
	"necromanciarr/internal/torznab"
)

const (
	// maxFallbackDownloads borne les téléchargements d'un candidat par exécution, tous indexeurs confondus : avec
	// maxDownloadsPerRun à 10, au moins trois recherches de candidat sont servies. Un élément dont l'attribut infohash diffère ne
	// coûte aucun téléchargement ; au-delà de ce plafond, le candidat reste introuvable (pas de nouvel essai en boucle).
	maxFallbackDownloads = 3
	// maxFallbackQueryRunes borne q=<nom de la release> envoyé aux indexeurs.
	maxFallbackQueryRunes = 256
	fallbackSearchMode    = "search"
)

// ErrSecretKeyMissing : un .torrent d'indexeur porte une passkey et ne se garde que scellé ; sans
// NECROMANCIARR_SECRET_KEY, le secours n'interroge aucun indexeur.
var ErrSecretKeyMissing = errors.New("sources: indexer torrents need NECROMANCIARR_SECRET_KEY")

// FallbackError : ni le fournisseur ni les indexeurs du tracker n'ont rendu le .torrent de cet infohash. Elle
// rend libraryprovider.ErrNotFound (Unwrap) et ne porte que des identifiants de source et des erreurs typées, jamais de
// lien, de clé, de nom de release ni de contenu.
type FallbackError struct {
	// Searched liste les sources interrogées, fournisseur en tête.
	Searched []ID
	// Skipped porte les refus d'éléments (autre infohash, v2 seul, hôte étranger…) : la recherche a continué.
	Skipped []error
	// Interrupted porte les arrêts d'un indexeur (budget épuisé, indisponible, accès refusé, réponse hostile).
	Interrupted []error
	// MagnetOnly compte les résultats sans lien .torrent.
	MagnetOnly int
}

func (*FallbackError) Error() string {
	return "sources: torrent not found at the provider nor at the indexers of its tracker"
}

// Unwrap rend libraryprovider.ErrNotFound : le candidat reste torrent_not_found.
func (*FallbackError) Unwrap() error { return libraryprovider.ErrNotFound }

// Incomplete est vrai si un indexeur n'a pas pu être interrogé jusqu'au bout : la recherche est à refaire plus tard.
func (fallback *FallbackError) Incomplete() bool { return len(fallback.Interrupted) > 0 }

// SetFallbackTrackers déclare le tracker de chaque indexeur (config torznab[].tracker), à poser au câblage avant
// tout usage ; un indexeur sans tracker n'est jamais utilisé en secours.
func (chain *Chain) SetFallbackTrackers(trackers map[ID]string) {
	chain.fallbackTrackers = maps.Clone(trackers)
}

// fallbackIndexers rend, dans l'ordre de la chaîne, les indexeurs dont le tracker égale celui du candidat sous
// config.NormalizeTracker.
func (chain *Chain) fallbackIndexers(tracker string) []ID {
	wanted := config.NormalizeTracker(tracker)
	if wanted == "" {
		return nil
	}
	var matching []ID
	for _, identifier := range chain.order[1:] {
		if config.NormalizeTracker(chain.fallbackTrackers[identifier]) == wanted {
			matching = append(matching, identifier)
		}
	}
	return matching
}

// fetchFromIndexers cherche le .torrent chez les indexeurs du tracker, un à un, et s'arrête au premier élément
// retenu ; une annulation arrête tout.
func (chain *Chain) fetchFromIndexers(ctx context.Context, release Release, budget *Budget, indexers []ID, query string) (Fetched, error) {
	search := &fallbackSearch{chain: chain, budget: budget, release: release, query: query, downloadsLeft: maxFallbackDownloads}
	search.outcome.Searched = []ID{ProviderID}
	for _, indexer := range indexers {
		if search.downloadsLeft == 0 {
			break
		}
		search.outcome.Searched = append(search.outcome.Searched, indexer)
		fetched, found := search.indexer(ctx, indexer)
		if found {
			return fetched, nil
		}
		if err := ctx.Err(); err != nil {
			return Fetched{}, err
		}
	}
	return Fetched{}, &search.outcome
}

// fallbackSearch est le secours d'un candidat : un seul plafond de téléchargements pour tous ses indexeurs.
type fallbackSearch struct {
	chain         *Chain
	budget        *Budget
	release       Release
	query         string
	downloadsLeft int
	outcome       FallbackError
}

func (search *fallbackSearch) indexer(ctx context.Context, indexer ID) (Fetched, bool) {
	results, err := search.results(ctx, indexer)
	if err != nil {
		search.outcome.Interrupted = append(search.outcome.Interrupted, err)
		return Fetched{}, false
	}
	search.outcome.MagnetOnly += results.MagnetOnly
	for _, item := range search.candidateItems(results.Items) {
		if search.downloadsLeft == 0 {
			return Fetched{}, false
		}
		search.downloadsLeft--
		fetched, err := search.download(ctx, indexer, item)
		if err == nil {
			return fetched, true
		}
		if stopsTheIndexer(err) {
			search.outcome.Interrupted = append(search.outcome.Interrupted, err)
			return Fetched{}, false
		}
		search.outcome.Skipped = append(search.outcome.Skipped, err)
	}
	return Fetched{}, false
}

// candidateItems met d'abord les éléments qui annoncent l'infohash du candidat, puis ceux sans attribut dans l'ordre
// du flux ; un élément d'un autre infohash n'est jamais retenu.
func (search *fallbackSearch) candidateItems(items []torznab.Item) []torznab.Item {
	var announced, unannounced []torznab.Item
	for _, item := range items {
		switch item.InfoHash {
		case search.release.Infohash:
			announced = append(announced, item)
		case "":
			unannounced = append(unannounced, item)
		}
	}
	return append(announced, unannounced...)
}

func (search *fallbackSearch) results(ctx context.Context, indexer ID) (torznab.Results, error) {
	client := search.chain.indexers[indexer]
	var results torznab.Results
	err := search.chain.Call(ctx, search.budget, indexer, KindQuery, func(requestCtx context.Context) error {
		var searchErr error
		results, searchErr = client.Search(requestCtx, torznab.Query{Mode: fallbackSearchMode, Text: search.query})
		search.budget.CountMagnetOnly(indexer, results.MagnetOnly)
		return searchErr
	})
	return results, err
}

// download télécharge l'élément puis n'en garde que la copie assainie et la copie scellée ; le tampon en clair est
// effacé avant de rendre la main (au mieux : Go a pu en copier ailleurs).
func (search *fallbackSearch) download(ctx context.Context, indexer ID, item torznab.Item) (Fetched, error) {
	client := search.chain.indexers[indexer]
	var raw []byte
	err := search.chain.Call(ctx, search.budget, indexer, KindDownload, func(requestCtx context.Context) error {
		var downloadErr error
		raw, downloadErr = client.Download(requestCtx, item)
		return downloadErr
	})
	defer clear(raw)
	if err != nil {
		return Fetched{}, err
	}
	return search.keep(indexer, raw)
}

// keep vérifie l'infohash calculé (l'attribut de l'élément ne suffit jamais), assainit, contrôle que l'assainissement
// n'a pas changé l'infohash, puis scelle avec la liaison des valeurs enregistrées sur la ligne du candidat ; quand une
// entrée trackers donne l'annonce, rien n'est scellé (la passkey de l'indexeur ne servirait jamais).
func (search *fallbackSearch) keep(indexer ID, raw []byte) (Fetched, error) {
	if len(raw) > store.MaxTorrentBytes {
		return Fetched{}, ErrTorrentTooLarge
	}
	if err := requireInfohash(raw, search.release.Infohash); err != nil {
		return Fetched{}, err
	}
	sanitized, err := Sanitize(raw)
	if err != nil {
		return Fetched{}, err
	}
	if err := requireInfohash(sanitized, search.release.Infohash); err != nil {
		return Fetched{}, err
	}
	if search.release.AnnounceConfigured {
		return Fetched{Source: indexer, Sanitized: sanitized}, nil
	}
	sealed, err := search.chain.sealer.Seal(raw, Binding{Infohash: search.release.Infohash, Tracker: search.release.Tracker, Source: indexer})
	if err != nil {
		return Fetched{}, err
	}
	return Fetched{Source: indexer, Sanitized: sanitized, Sealed: sealed}, nil
}

// requireInfohash calcule l'infohash v1 ; l'erreur ne recopie rien du .torrent (torrentcheck peut citer un nom hostile).
func requireInfohash(raw []byte, wanted string) error {
	parsed, err := torrentcheck.Parse(raw)
	if errors.Is(err, torrentcheck.ErrV2Only) {
		return torrentcheck.ErrV2Only
	}
	if err != nil {
		return ErrInvalidTorrent
	}
	if parsed.InfoHash != wanted {
		return fmt.Errorf("%w: computed infohash differs", torznab.ErrInfohashMismatch)
	}
	return nil
}

// stopsTheIndexer distingue un indexeur à laisser pour cette exécution (budget, indisponibilité, refus, annulation)
// d'un élément simplement refusé.
func stopsTheIndexer(err error) bool {
	return errors.Is(err, ErrBudgetExhausted) || errors.Is(err, torznab.ErrUnavailable) || errors.Is(err, torznab.ErrAccessDenied) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// fallbackQueryText retire les caractères de contrôle du nom de la release et le borne à maxFallbackQueryRunes.
func fallbackQueryText(name string) string {
	cleaned := strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return -1
		}
		return character
	}, name)
	runes := []rune(strings.TrimSpace(cleaned))
	if len(runes) > maxFallbackQueryRunes {
		runes = runes[:maxFallbackQueryRunes]
	}
	return strings.TrimSpace(string(runes))
}

// fallbackEligible dit si le candidat peut être cherché chez les indexeurs : infohash et tracker acceptables pour
// la liaison du scellement, nom de release non vide.
func fallbackEligible(release Release, query string) bool {
	return isInfohash(release.Infohash) && isPlainComponent(release.Tracker) && query != ""
}
