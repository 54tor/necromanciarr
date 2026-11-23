package hashsource

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const (
	// maxRCResponseBytes borne la réponse de rc : un seul élément de listage.
	maxRCResponseBytes = 256 << 10
	rcMethodStat       = "operations/stat"
)

// rcAllowedMethods est la liste fermée des méthodes rc appelées : lecture seule, jamais core/command, job/*, sync/*,
// ni une opération qui écrit. Le nom de méthode n'est jamais lu d'une entrée.
var rcAllowedMethods = map[string]bool{rcMethodStat: true}

var errRedirectRefused = errors.New("redirect refused")

// RcOptions règle l'adaptateur qui interroge l'API de contrôle à distance (--rc) du processus rclone qui porte le
// montage. URL peut porter « utilisateur:motdepasse@ », retiré de l'URL puis envoyé en authentification Basic.
type RcOptions struct {
	URL        string
	Mounts     []Mount
	Timeout    time.Duration
	HTTPClient *http.Client
}

type rcloneRC struct {
	endpoint string
	user     string
	password string
	hasAuth  bool
	mounts   mountTable
	timeout  time.Duration
	client   *http.Client
}

// NewRcloneRC renvoie la source qui interroge `operations/stat` du processus rclone distant ; aucune donnée n'est lue.
// Les redirections sont refusées pour que les identifiants n'aillent jamais vers un autre hôte.
func NewRcloneRC(options RcOptions) (HashSource, error) {
	table, err := newMountTable(options.Mounts)
	if err != nil {
		return nil, err
	}
	// Message fixe : l'URL brute peut contenir le mot de passe.
	parsedURL, err := url.Parse(options.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, errors.New("hashsource: invalid rclone rc url")
	}
	source := &rcloneRC{mounts: table, timeout: options.Timeout, client: refusingRedirects(options.HTTPClient)}
	if parsedURL.User != nil {
		source.user = parsedURL.User.Username()
		source.password, _ = parsedURL.User.Password()
		source.hasAuth = true
	}
	parsedURL.User, parsedURL.RawQuery, parsedURL.Fragment = nil, "", ""
	source.endpoint = strings.TrimRight(parsedURL.String(), "/")
	if source.timeout <= 0 {
		source.timeout = defaultTimeout
	}
	return source, nil
}

// refusingRedirects copie le client fourni (TLS et transport conservés) en lui interdisant de suivre une redirection.
func refusingRedirects(provided *http.Client) *http.Client {
	client := &http.Client{}
	if provided != nil {
		copied := *provided
		client = &copied
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errRedirectRefused }
	return client
}

type rcStatRequest struct {
	Fs     string       `json:"fs"`
	Remote string       `json:"remote"`
	Opt    rcStatOption `json:"opt"`
}

type rcStatOption struct {
	ShowHash   bool     `json:"showHash"`
	HashTypes  []string `json:"hashTypes"`
	NoMimeType bool     `json:"noMimeType"`
}

type rcStatResponse struct {
	Item *struct {
		Name   string            `json:"Name"`
		IsDir  bool              `json:"IsDir"`
		Hashes map[string]string `json:"Hashes"`
	} `json:"item"`
}

// Hash demande à rc le listage du seul fichier avec ses empreintes ; un fichier absent du remote ou sans cette
// empreinte donne Available=false.
func (source *rcloneRC) Hash(ctx context.Context, filePath string, algorithm Algorithm) (Result, error) {
	if err := checkAlgorithm(algorithm); err != nil {
		return Result{}, err
	}
	mount, relative, err := source.mounts.resolve(filePath)
	if err != nil {
		return Result{}, err
	}
	hashType := strings.ToLower(string(algorithm))
	request := rcStatRequest{Fs: mount.Remote, Remote: relative, Opt: rcStatOption{ShowHash: true, HashTypes: []string{hashType}, NoMimeType: true}}
	var response rcStatResponse
	if err := source.call(ctx, rcMethodStat, request, &response); err != nil {
		return Result{}, err
	}
	return interpretStat(response, hashType, algorithm, path.Base(relative))
}

func interpretStat(response rcStatResponse, hashType string, algorithm Algorithm, fileName string) (Result, error) {
	item := response.Item
	if item == nil || item.IsDir {
		return Result{}, nil
	}
	if item.Name != fileName {
		return Result{}, fmt.Errorf("%w: unexpected item", ErrRcloneFailed)
	}
	value := strings.ToLower(item.Hashes[hashType])
	if value == "" {
		return Result{}, nil
	}
	if _, err := hex.DecodeString(value); err != nil || len(value) != algorithm.hexLength() {
		return Result{}, fmt.Errorf("%w: unexpected hash", ErrRcloneFailed)
	}
	return Result{Value: value, Origin: Backend, Available: true}, nil
}

// call envoie un POST JSON ; toutes les erreurs ont un message fixe : ni corps de réponse, ni URL, ni identifiant.
func (source *rcloneRC) call(ctx context.Context, method string, request, response any) error {
	if !rcAllowedMethods[method] {
		return fmt.Errorf("%w: method not allowed", ErrRcloneFailed)
	}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("%w: cannot encode the request", ErrRcloneFailed)
	}
	callContext, cancel := context.WithTimeout(ctx, source.timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(callContext, http.MethodPost, source.endpoint+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: cannot build the request", ErrRcloneFailed)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if source.hasAuth {
		httpRequest.SetBasicAuth(source.user, source.password)
	}
	httpResponse, err := source.client.Do(httpRequest) // #nosec G704 -- URL de la configuration de l'opérateur, méthode d'une liste fermée
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: request failed", ErrRcloneFailed)
	}
	defer func() { _ = httpResponse.Body.Close() }()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode > 299 {
		return fmt.Errorf("%w: http status %d", ErrRcloneFailed, httpResponse.StatusCode)
	}
	return decodeStrictly(httpResponse.Body, response)
}

// decodeStrictly lit au plus maxRCResponseBytes et exige une seule valeur JSON.
func decodeStrictly(body io.Reader, response any) error {
	limited := io.LimitReader(body, maxRCResponseBytes+1)
	content, err := io.ReadAll(limited)
	if err != nil || len(content) > maxRCResponseBytes {
		return fmt.Errorf("%w: unreadable or oversized response", ErrRcloneFailed)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("%w: invalid response", ErrRcloneFailed)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: invalid response", ErrRcloneFailed)
	}
	return nil
}

// String et GoString n'exposent ni l'URL ni le mot de passe, quel que soit le verbe de fmt ou l'appel de journal.
func (source *rcloneRC) String() string { return "rclone rc source" }

// GoString : voir String.
func (source *rcloneRC) GoString() string { return source.String() }
