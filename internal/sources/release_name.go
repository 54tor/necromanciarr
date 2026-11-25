package sources

import (
	"fmt"
	"slices"
	"strings"

	"necromanciarr/internal/config"
	"necromanciarr/internal/torrentcheck"
)

// releaseName choisit le nom enregistré d'un candidat d'indexeur sans jamais garder une passkey de son annonce : le
// titre de l'indexeur qui en contient une cède la place au nom du torrent ; un torrent dont le nom ou un chemin en
// contient une est refusé (ErrInvalidTorrent), ces champs appartenant à l'infohash. Une valeur de moins de
// config.MinKnownSecretLength octets ne compte pas, comme au masquage hors URL.
func releaseName(raw []byte, title string, parsed *torrentcheck.Torrent) (string, error) {
	passkeys := announcedPasskeys(raw)
	if carriesPasskey(parsed.Root, passkeys) || slices.ContainsFunc(parsed.Files, func(file torrentcheck.File) bool {
		return carriesPasskey(file.Path, passkeys)
	}) {
		return "", fmt.Errorf("%w: torrent name or path carries an announce passkey", ErrInvalidTorrent)
	}
	name := fallbackQueryText(title)
	if name == "" || carriesPasskey(name, passkeys) {
		return parsed.Root, nil
	}
	return name, nil
}

// announcedPasskeys rend, en minuscules, les valeurs d'annonce assez longues pour être masquées partout ; une annonce
// illisible n'en donne aucune.
func announcedPasskeys(raw []byte) []string {
	tiers, err := AnnounceTiers(raw)
	if err != nil {
		return nil
	}
	var passkeys []string
	for _, value := range DiscoveredSecretValues(tiers) {
		if len(value) >= config.MinKnownSecretLength {
			passkeys = append(passkeys, strings.ToLower(value))
		}
	}
	return passkeys
}

// carriesPasskey compare, casse ignorée, le texte brut et décodé comme le masquage (« %66%61… ») à chaque passkey.
func carriesPasskey(text string, passkeys []string) bool {
	forms := []string{strings.ToLower(text), strings.ToLower(config.DecodePercent(text))}
	return slices.ContainsFunc(passkeys, func(passkey string) bool {
		return slices.ContainsFunc(forms, func(form string) bool { return strings.Contains(form, passkey) })
	})
}
