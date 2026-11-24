package torznab

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/idna"

	"necromanciarr/internal/torrentcheck"
)

const (
	// maxTorrentBytes plafonne un .torrent téléchargé, comme la colonne torrent ; il est compté après décompression.
	maxTorrentBytes = 8 << 20
	acceptTorrent   = "application/x-bittorrent, application/octet-stream"
	maxPort         = 65535
)

var (
	// ErrForeignDownloadHost signale un lien de téléchargement hors de l'origine (schéma, hôte, port) de l'indexeur,
	// ou porteur d'identifiants : rien n'est demandé.
	ErrForeignDownloadHost = errors.New("torznab: download link outside the indexer origin")
	// ErrInfohashMismatch signale un .torrent dont l'infohash v1 calculé diffère de l'attribut infohash du résultat.
	ErrInfohashMismatch = errors.New("torznab: downloaded torrent does not match the announced infohash")
)

// torrentContentTypes sont les seuls types acceptés pour un .torrent, paramètres ignorés.
var torrentContentTypes = map[string]bool{"application/x-bittorrent": true, "application/octet-stream": true}

// defaultPorts rend explicite le port implicite de chaque schéma accepté.
var defaultPorts = map[string]int{"http": 80, "https": 443}

// Download télécharge le .torrent d'un résultat, depuis l'origine de l'indexeur seulement, sous le même limiteur que
// la recherche. Les octets rendus sont bruts : leur annonce porte souvent une passkey, l'appelant les scelle et ne
// les journalise jamais.
func (client *Client) Download(ctx context.Context, item Item) ([]byte, error) {
	target, err := client.downloadTarget(item.downloadURL)
	if err != nil {
		return nil, err
	}
	content, err := client.fetchTorrent(ctx, target)
	if err != nil {
		return nil, err
	}
	if err := checkTorrent(content, item.InfoHash); err != nil {
		return nil, err
	}
	return content, nil
}

// downloadTarget résout le lien contre l'URL de l'indexeur et n'accepte que sa propre origine ; l'hôte demandé est
// la forme canonique comparée, pour que le transport ne puisse pas lire le lien autrement que la comparaison.
func (client *Client) downloadTarget(rawLink string) (string, error) {
	if rawLink == "" || strings.ContainsFunc(rawLink, isUnsafeLinkRune) {
		return "", ErrForeignDownloadHost
	}
	resolved, err := client.base.Parse(rawLink)
	if err != nil {
		return "", ErrForeignDownloadHost
	}
	linkOrigin, isLinkValid := originOf(resolved)
	baseOrigin, isBaseValid := originOf(client.base)
	if !isLinkValid || !isBaseValid || linkOrigin != baseOrigin {
		return "", ErrForeignDownloadHost
	}
	resolved.Scheme = linkOrigin.scheme
	resolved.Host = linkOrigin.hostPort()
	resolved.Fragment, resolved.RawFragment = "", ""
	client.addAPIKey(resolved)
	return resolved.String(), nil
}

// isUnsafeLinkRune refuse la barre oblique inverse (lue comme « / » par certains analyseurs), les espaces et les
// caractères de contrôle.
func isUnsafeLinkRune(character rune) bool {
	return character == '\\' || unicode.IsSpace(character) || unicode.IsControl(character)
}

// addAPIKey ajoute la clé de l'indexeur au lien qui n'en porte pas ; la requête d'origine est gardée telle quelle.
func (client *Client) addAPIKey(link *url.URL) {
	if client.apiKey == "" || link.Query().Has(apiKeyParameter) {
		return
	}
	keyParameter := apiKeyParameter + "=" + url.QueryEscape(client.apiKey)
	if link.RawQuery == "" {
		link.RawQuery = keyParameter
		return
	}
	link.RawQuery += "&" + keyParameter
}

// origin est le triplet comparé : schéma en minuscules, hôte canonique, port explicite.
type origin struct {
	scheme string
	host   string
	port   int
}

func (linkOrigin origin) hostPort() string {
	if linkOrigin.port != defaultPorts[linkOrigin.scheme] {
		return net.JoinHostPort(linkOrigin.host, strconv.Itoa(linkOrigin.port))
	}
	if strings.Contains(linkOrigin.host, ":") {
		return "[" + linkOrigin.host + "]"
	}
	return linkOrigin.host
}

// originOf refuse identifiants, URL opaques et schémas autres que http(s).
func originOf(parsed *url.URL) (origin, bool) {
	scheme := strings.ToLower(parsed.Scheme)
	defaultPort, isHTTP := defaultPorts[scheme]
	hasHiddenParts := parsed.User != nil || parsed.Opaque != ""
	if !isHTTP || hasHiddenParts {
		return origin{}, false
	}
	host, isValidHost := canonicalHost(parsed.Hostname())
	port, isValidPort := explicitPort(parsed.Port(), defaultPort)
	if !isValidHost || !isValidPort {
		return origin{}, false
	}
	return origin{scheme: scheme, host: host, port: port}, true
}

// canonicalHost rend une adresse IP sous sa forme canonique (zone IPv6 refusée), ou un nom en ASCII (punycode)
// minuscule sans point final.
func canonicalHost(hostname string) (string, bool) {
	if hostname == "" {
		return "", false
	}
	if address, err := netip.ParseAddr(hostname); err == nil {
		return address.String(), address.Zone() == ""
	}
	ascii, err := idna.Lookup.ToASCII(strings.TrimSuffix(hostname, "."))
	if err != nil || ascii == "" {
		return "", false
	}
	return ascii, true
}

func explicitPort(rawPort string, defaultPort int) (int, bool) {
	if rawPort == "" {
		return defaultPort, true
	}
	port, err := strconv.Atoi(rawPort)
	return port, err == nil && port > 0 && port <= maxPort
}

// fetchTorrent refuse tout statut hors 2xx et tout type inattendu avant de lire le corps, plafonné à
// maxTorrentBytes.
func (client *Client) fetchTorrent(ctx context.Context, target string) ([]byte, error) {
	response, cancel, err := client.send(ctx, target, acceptTorrent)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer func() { _ = response.Body.Close() }()
	if _, err := client.checkStatus(response); err != nil {
		return nil, err
	}
	if !isTorrentContentType(response.Header.Get("Content-Type")) {
		return nil, fmt.Errorf("%w: content type is not a torrent", ErrBadResponse)
	}
	return readCapped(response.Body, maxTorrentBytes)
}

func isTorrentContentType(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	return err == nil && torrentContentTypes[mediaType]
}

// checkTorrent valide le .torrent ; le détail de torrentcheck n'est pas recopié, il peut citer un nom hostile.
func checkTorrent(content []byte, announcedInfoHash string) error {
	parsed, err := torrentcheck.Parse(content)
	if errors.Is(err, torrentcheck.ErrV2Only) {
		return fmt.Errorf("%w: %w", ErrBadResponse, torrentcheck.ErrV2Only)
	}
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBadResponse, torrentcheck.ErrInvalidTorrent)
	}
	expectedInfoHash := strings.ToLower(announcedInfoHash)
	if expectedInfoHash != "" && parsed.InfoHash != expectedInfoHash {
		return ErrInfohashMismatch
	}
	return nil
}
