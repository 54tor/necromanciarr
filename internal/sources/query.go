package sources

// ProviderID identifie le fournisseur de bibliothèque dans la chaîne ; il est toujours la première source.
const ProviderID ID = "provider"

// Release désigne un .torrent à obtenir : l'infohash est la clé, le tracker précise la variante.
type Release struct {
	Infohash, Tracker, Name string
	// AnnounceConfigured : une entrée trackers couvre le tracker (la configuration gagne au seed) ; un .torrent
	// d'indexeur n'a alors pas besoin d'être scellé.
	AnnounceConfigured bool
}

// Fetched est un .torrent obtenu : copie assainie (sans annonce) et copie scellée complète.
type Fetched struct {
	Source ID
	// Sanitized sert à Verify, à l'affichage et au cache : info et champs non secrets.
	Sanitized []byte
	// Sealed est le .torrent complet chiffré ; nil quand la source n'a rien de secret (le fournisseur).
	Sealed []byte
}

// MediaQuery décrit un média de la bibliothèque à chercher chez les indexeurs.
type MediaQuery struct {
	TMDBID, TVDBID int
	IMDBID, Title  string
	Year           int
	Season         int
	// Episode vaut 0 pour un groupe de plusieurs fichiers (pack de saison) ; Season et Episode à 0 : inconnus.
	Episode int
	// Series distingue une série (Sonarr, t=tvsearch) d'un film (Radarr, t=movie).
	Series bool
	// Sizes sont les tailles exactes des fichiers locaux du groupe : la preuve de taille et le seuil bas s'y réfèrent.
	Sizes []int64
	// Uncovered sont les tailles des fichiers encore sans candidat de taille exacte : la recherche s'arrête quand
	// toutes sont couvertes.
	Uncovered []int64
	// AnnounceConfigured dit si une entrée trackers couvre ce tracker (même règle que Verify et Seed) : rien n'est
	// alors scellé ; nil vaut jamais.
	AnnounceConfigured func(tracker string) bool
}

// Found est un résultat d'indexeur retenu après le filtre de taille ; il porte déjà son .torrent.
type Found struct {
	Source    ID
	Release   Release
	TotalSize int64
	Fetched   Fetched
}

// IndexerReport résume ce qu'une exécution a demandé à un indexeur : des comptes, jamais d'URL ni de clé.
type IndexerReport struct {
	Queries         int  `json:"torznab_queries"`
	Downloads       int  `json:"torznab_downloads"`
	MagnetOnly      int  `json:"magnet_only"`
	BudgetExhausted bool `json:"budget_exhausted"`
}

// Report associe chaque indexeur sollicité à son résumé.
type Report map[ID]IndexerReport
