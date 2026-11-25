package sources

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"

	"necromanciarr/internal/torznab"
)

const (
	textSearchMode  = "search"
	movieSearchMode = "movie"
	tvSearchMode    = "tvsearch"
	// movieSlackBytes et movieSlackDivisor bornent un film seul : la taille du fichier plus 1 % et 64 Mio d'annexes
	// (sous-titres, .nfo, échantillon) ; un pack de série n'a pas de plafond.
	movieSlackBytes   = int64(64) << 20
	movieSlackDivisor = 100
)

// categoryRange est une plage de catégories Newznab : 2000 à 2999 pour les films, 5000 à 5999 pour les séries.
type categoryRange struct{ first, end int }

var (
	movieCategoryRange = categoryRange{first: 2000, end: 3000}
	tvCategoryRange    = categoryRange{first: 5000, end: 6000}
)

// searchCategories garde les catégories annoncées par les capacités : celles configurées qui y figurent, sinon celles
// du genre du média (films ou séries). searchable est faux quand rien ne reste : l'indexeur n'est pas interrogé.
// Des capacités sans catégorie n'excluent rien : la configuration passe telle quelle, sinon aucun filtre.
func searchCategories(advertised, configured []int, series bool) (categories []int, searchable bool) {
	if len(advertised) == 0 {
		return slices.Clone(configured), true
	}
	wanted := func(category int) bool { return slices.Contains(configured, category) }
	if len(configured) == 0 {
		kind := movieCategoryRange
		if series {
			kind = tvCategoryRange
		}
		wanted = func(category int) bool { return category >= kind.first && category < kind.end }
	}
	for _, category := range advertised {
		if wanted(category) && !slices.Contains(categories, category) {
			categories = append(categories, category)
		}
	}
	return categories, len(categories) > 0
}

// planQueries donne les recherches d'un groupe, dans l'ordre : par identifiant si les capacités l'annoncent, puis par
// texte. Saison et épisode ne partent que connus (> 0) : jamais season=0&ep=0 ni S00E00 tirés de valeurs inconnues.
func planQueries(caps torznab.Caps, media MediaQuery, categories []int) []torznab.Query {
	var queries []torznab.Query
	if query, planned := idQuery(caps, media); planned {
		queries = append(queries, query)
	}
	if query, planned := textQuery(caps, media); planned {
		queries = append(queries, query)
	}
	for queryIndex := range queries {
		queries[queryIndex].Categories = slices.Clone(categories)
	}
	return queries
}

func idQuery(caps torznab.Caps, media MediaQuery) (torznab.Query, bool) {
	if media.Series {
		return seriesIDQuery(caps.Modes[tvSearchMode], media)
	}
	params := caps.Modes[movieSearchMode]
	switch {
	case media.TMDBID > 0 && slices.Contains(params, "tmdbid"):
		return torznab.Query{Mode: movieSearchMode, TMDBID: media.TMDBID}, true
	case media.IMDBID != "" && slices.Contains(params, "imdbid"):
		return torznab.Query{Mode: movieSearchMode, IMDBID: media.IMDBID}, true
	}
	return torznab.Query{}, false
}

func seriesIDQuery(params []string, media MediaQuery) (torznab.Query, bool) {
	if media.TVDBID <= 0 || !slices.Contains(params, "tvdbid") {
		return torznab.Query{}, false
	}
	query := torznab.Query{Mode: tvSearchMode, TVDBID: media.TVDBID}
	if media.Season > 0 && slices.Contains(params, "season") {
		query.Season = media.Season
		if media.Episode > 0 && slices.Contains(params, "ep") {
			query.Episode = media.Episode
		}
	}
	return query, true
}

// textQuery : q=<titre> <année> (film), q=<titre> S<ss>E<ee> ou S<ss> (série) ; le titre vient de Radarr/Sonarr, déjà
// nettoyé, et repasse par le même filtre que le secours de Verify.
func textQuery(caps torznab.Caps, media MediaQuery) (torznab.Query, bool) {
	title := fallbackQueryText(media.Title)
	if _, available := caps.Modes[textSearchMode]; !available || title == "" {
		return torznab.Query{}, false
	}
	switch {
	case !media.Series && media.Year > 0:
		title += " " + strconv.Itoa(media.Year)
	case media.Series && media.Season > 0 && media.Episode > 0:
		title += fmt.Sprintf(" S%02dE%02d", media.Season, media.Episode)
	case media.Series && media.Season > 0:
		title += fmt.Sprintf(" S%02d", media.Season)
	}
	return torznab.Query{Mode: textSearchMode, Text: title}, true
}

// eligibleItems applique le filtre local avant tout téléchargement (Torznab ne donne que la taille totale) : au moins
// la taille du plus gros fichier du groupe ; pour un film seul, au plus cette taille + 1 % + 64 Mio. Les résultats
// gardés sont triés par écart de taille, le plus proche d'abord.
func eligibleItems(items []torznab.Item, media MediaQuery) []torznab.Item {
	if len(media.Sizes) == 0 {
		return nil
	}
	largest := slices.Max(media.Sizes)
	singleMovie := !media.Series && len(media.Sizes) == 1
	upper := largest + largest/movieSlackDivisor + movieSlackBytes
	var eligible []torznab.Item
	for _, item := range items {
		tooLarge := singleMovie && item.Size > upper
		if item.Size >= largest && !tooLarge {
			eligible = append(eligible, item)
		}
	}
	slices.SortStableFunc(eligible, func(left, right torznab.Item) int { return cmp.Compare(left.Size, right.Size) })
	return eligible
}
