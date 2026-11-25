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
	Episode        int
	// Sizes sont les tailles exactes des fichiers locaux du groupe.
	Sizes []int64
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
	Queries, Downloads, MagnetOnly int
	BudgetExhausted                bool
}

// Report associe chaque indexeur sollicité à son résumé.
type Report map[ID]IndexerReport
