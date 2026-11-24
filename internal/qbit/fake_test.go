package qbit_test

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Valeurs du faux qBittorrent : identifiants et cookie de test, jamais réels.
const (
	fakeUser       = "alice"
	fakePassword   = "fake-password-value"
	fakeSessionID  = "fake-session-cookie-value"
	webAPIVersion4 = "2.8.19"
	webAPIVersion5 = "2.11.2"
	webAPIOld      = "2.8.3"
	webAPIJSONAdd  = "2.14.0"
	hashMovie      = "0123456789abcdef0123456789abcdef01234567"
	hashPack       = "89abcdef0123456789abcdef0123456789abcdef"
	maxUploadBytes = 1 << 20
)

// fakeTorrent est l'état d'un torrent côté faux client ; checkPollsLeft simule la durée du recontrôle.
type fakeTorrent struct {
	Name            string
	SavePath        string
	ContentPath     string
	Category        string
	Tags            string
	State           string
	Progress        float64
	Size            int64
	Files           []fakeFile
	ProgressAfter   float64
	StateAfter      string
	NoCheckingState bool
	FilesMissing    bool
	RecheckFails    bool
	Export          []byte
	checkPollsLeft  int
	checkPollsTotal int
}

type fakeFile struct {
	Name     string  `json:"name"`
	Size     int64   `json:"size"`
	Progress float64 `json:"progress"`
}

// recordedAdd garde les champs reçus par torrents/add.
type recordedAdd struct {
	Fields      map[string]string
	TorrentData []byte
	FileName    string
}

// fakeQbit simule l'API Web v2 de qBittorrent 4 ou 5 derrière un préfixe de chemin optionnel.
type fakeQbit struct {
	mutex          sync.Mutex
	version        string
	prefix         string
	requireAuth    bool
	logins         int
	actions        []string
	adds           []recordedAdd
	torrents       map[string]*fakeTorrent
	referers       []string
	origins        []string
	forcedStatus   int
	forcedBody     string
	loginsRejected bool
	sessionExpired bool
	addRejected    bool
	// infoQueries garde la requête de chaque appel à torrents/info ; maxServed le plus grand nombre d'entrées servies
	// en une réponse ; failInfoAt fait échouer (HTTP 500) l'appel de ce rang (1 = premier) ; onInfo est appelé à
	// chaque appel avec son rang.
	infoQueries []string
	maxServed   int
	failInfoAt  int
	onInfo      func(call int)
}

func newFakeQbit(version string) *fakeQbit {
	return &fakeQbit{version: version, torrents: map[string]*fakeTorrent{}}
}

func (fake *fakeQbit) start(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	return server
}

func (fake *fakeQbit) isV5() bool { return fake.version == webAPIVersion5 }

func (fake *fakeQbit) serve(writer http.ResponseWriter, request *http.Request) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	apiPath, hasPrefix := strings.CutPrefix(request.URL.Path, fake.prefix+"/api/v2/")
	if !hasPrefix {
		http.NotFound(writer, request)
		return
	}
	fake.referers = append(fake.referers, request.Header.Get("Referer"))
	fake.origins = append(fake.origins, request.Header.Get("Origin"))
	if apiPath == "auth/login" {
		fake.login(writer, request)
		return
	}
	if fake.requireAuth && !fake.hasSession(request) {
		writer.WriteHeader(http.StatusForbidden)
		return
	}
	if fake.forcedStatus != 0 && apiPath != "app/webapiVersion" {
		writer.WriteHeader(fake.forcedStatus)
		_, _ = io.WriteString(writer, fake.forcedBody)
		return
	}
	fake.route(writer, request, apiPath)
}

func (fake *fakeQbit) hasSession(request *http.Request) bool {
	cookie, err := request.Cookie("SID")
	return err == nil && cookie.Value == fakeSessionID && !fake.sessionExpired
}

func (fake *fakeQbit) login(writer http.ResponseWriter, request *http.Request) {
	fake.logins++
	if request.Method != http.MethodPost || request.ParseForm() != nil {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	credentialsMatch := request.PostForm.Get("username") == fakeUser && request.PostForm.Get("password") == fakePassword
	if fake.loginsRejected || !credentialsMatch {
		_, _ = io.WriteString(writer, "Fails.")
		return
	}
	fake.sessionExpired = false
	http.SetCookie(writer, &http.Cookie{Name: "SID", Value: fakeSessionID, Path: "/"})
	_, _ = io.WriteString(writer, "Ok.")
}

func (fake *fakeQbit) route(writer http.ResponseWriter, request *http.Request, apiPath string) {
	switch apiPath {
	case "app/webapiVersion":
		_, _ = io.WriteString(writer, fake.version)
	case "torrents/add":
		fake.add(writer, request)
	case "torrents/info":
		fake.info(writer, request)
	case "torrents/files":
		fake.files(writer, request)
	case "torrents/export":
		fake.export(writer, request)
	case "torrents/delete":
		fake.remove(writer, request)
	case "torrents/recheck", "torrents/pause", "torrents/resume", "torrents/stop", "torrents/start":
		fake.action(writer, request, strings.TrimPrefix(apiPath, "torrents/"))
	default:
		http.NotFound(writer, request)
	}
}

func (fake *fakeQbit) add(writer http.ResponseWriter, request *http.Request) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if request.Method != http.MethodPost || err != nil || mediaType != "multipart/form-data" {
		writer.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	if err := request.ParseMultipartForm(maxUploadBytes); err != nil {
		writer.WriteHeader(http.StatusBadRequest)
		return
	}
	recorded := recordedAdd{Fields: map[string]string{}}
	for fieldName, values := range request.MultipartForm.Value {
		recorded.Fields[fieldName] = values[0]
	}
	if fileHeaders := request.MultipartForm.File["torrents"]; len(fileHeaders) == 1 {
		recorded.TorrentData, recorded.FileName = readPart(fileHeaders[0])
	}
	fake.adds = append(fake.adds, recorded)
	fake.actions = append(fake.actions, "add")
	fake.answerAdd(writer)
}

// answerAdd répond en texte avant l'API 2.14, ensuite en JSON (success_count, failure_count).
func (fake *fakeQbit) answerAdd(writer http.ResponseWriter) {
	if fake.version == webAPIJSONAdd {
		failures := 0
		if fake.addRejected {
			failures = 1
		}
		writeJSON(writer, map[string]any{"added_torrent_ids": []string{}, "success_count": 1 - failures, "failure_count": failures})
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=UTF-8")
	if fake.addRejected {
		_, _ = io.WriteString(writer, "Fails.")
		return
	}
	_, _ = io.WriteString(writer, "Ok.")
}

func readPart(fileHeader *multipart.FileHeader) ([]byte, string) {
	file, err := fileHeader.Open()
	if err != nil {
		return nil, ""
	}
	defer func() { _ = file.Close() }()
	data, _ := io.ReadAll(file)
	return data, fileHeader.Filename
}

type fakeInfo struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	SavePath    string  `json:"save_path"`
	ContentPath string  `json:"content_path"`
	Category    string  `json:"category"`
	Tags        string  `json:"tags"`
	State       string  `json:"state"`
	Progress    float64 `json:"progress"`
	Size        int64   `json:"size"`
}

// info fait avancer la machine à états du recontrôle d'un pas par interrogation ; limit, offset et sort=hash
// découpent la liste comme qBittorrent.
func (fake *fakeQbit) info(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	fake.infoQueries = append(fake.infoQueries, request.URL.RawQuery)
	call := len(fake.infoQueries)
	if fake.onInfo != nil {
		fake.onInfo(call)
	}
	if call == fake.failInfoAt {
		writer.WriteHeader(http.StatusInternalServerError)
		return
	}
	wanted := query.Get("hashes")
	hashes := make([]string, 0, len(fake.torrents))
	for hash := range fake.torrents {
		if wanted == "" || wanted == hash {
			hashes = append(hashes, hash)
		}
	}
	slices.Sort(hashes)
	hashes = pageOf(hashes, query)
	list := make([]fakeInfo, 0, len(hashes))
	for _, hash := range hashes {
		torrent := fake.torrents[hash]
		fake.advanceCheck(torrent)
		list = append(list, fakeInfo{
			Hash: hash, Name: torrent.Name, SavePath: torrent.SavePath, ContentPath: torrent.ContentPath,
			Category: torrent.Category, Tags: torrent.Tags, State: torrent.State, Progress: torrent.Progress, Size: torrent.Size,
		})
	}
	fake.maxServed = max(fake.maxServed, len(list))
	writeJSON(writer, list)
}

func pageOf(hashes []string, query url.Values) []string {
	offset, _ := strconv.Atoi(query.Get("offset"))
	limit, err := strconv.Atoi(query.Get("limit"))
	start := min(max(offset, 0), len(hashes))
	if err != nil || limit <= 0 {
		return hashes[start:]
	}
	return hashes[start:min(start+limit, len(hashes))]
}

func (fake *fakeQbit) advanceCheck(torrent *fakeTorrent) {
	if !strings.HasPrefix(torrent.State, "checking") {
		return
	}
	if torrent.checkPollsLeft > 0 {
		torrent.checkPollsLeft--
		return
	}
	torrent.Progress = torrent.ProgressAfter
	torrent.State = fake.stoppedState(torrent.Progress)
	if torrent.StateAfter != "" {
		torrent.State = torrent.StateAfter
	}
}

func (fake *fakeQbit) stoppedState(progress float64) string {
	verb := "paused"
	if fake.isV5() {
		verb = "stopped"
	}
	if progress == 1 {
		return verb + "UP"
	}
	return verb + "DL"
}

func (fake *fakeQbit) files(writer http.ResponseWriter, request *http.Request) {
	torrent, found := fake.torrents[request.URL.Query().Get("hash")]
	if !found || torrent.FilesMissing {
		http.NotFound(writer, request)
		return
	}
	writeJSON(writer, torrent.Files)
}

func (fake *fakeQbit) export(writer http.ResponseWriter, request *http.Request) {
	hash := request.URL.Query().Get("hash")
	torrent, found := fake.torrents[hash]
	if !found {
		http.NotFound(writer, request)
		return
	}
	if torrent.Export != nil {
		_, _ = writer.Write(torrent.Export)
		return
	}
	_, _ = io.WriteString(writer, "d4:infod4:name"+fmt.Sprint(len(hash))+":"+hash+"ee")
}

func (fake *fakeQbit) action(writer http.ResponseWriter, request *http.Request, verb string) {
	if request.Method != http.MethodPost || request.ParseForm() != nil {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	v5Only := verb == "stop" || verb == "start"
	v4Only := verb == "pause" || verb == "resume"
	if (fake.isV5() && v4Only) || (!fake.isV5() && v5Only) {
		http.NotFound(writer, request)
		return
	}
	hash := request.PostForm.Get("hashes")
	fake.actions = append(fake.actions, verb+":"+hash)
	torrent, found := fake.torrents[hash]
	if !found {
		return
	}
	switch verb {
	case "recheck":
		if torrent.RecheckFails {
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		if torrent.NoCheckingState {
			return
		}
		torrent.State = "checkingUP"
		torrent.checkPollsLeft = torrent.checkPollsTotal
	case "pause", "stop":
		torrent.State = fake.stoppedState(torrent.Progress)
	case "resume", "start":
		torrent.State = "uploading"
	}
}

// remove enregistre torrents/delete avec la valeur de deleteFiles reçue.
func (fake *fakeQbit) remove(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.ParseForm() != nil {
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	hash := request.PostForm.Get("hashes")
	fake.actions = append(fake.actions, "delete:"+hash+":deleteFiles="+request.PostForm.Get("deleteFiles"))
	delete(fake.torrents, hash)
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func (fake *fakeQbit) snapshotActions() []string {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]string(nil), fake.actions...)
}

func (fake *fakeQbit) torrentState(hash string) (string, float64) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	torrent := fake.torrents[hash]
	return torrent.State, torrent.Progress
}

// Accès verrouillés : le gestionnaire HTTP tourne dans une autre goroutine.
func (fake *fakeQbit) update(change func(locked *fakeQbit)) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	change(fake)
}

func (fake *fakeQbit) loginCount() int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return fake.logins
}

func (fake *fakeQbit) addsSnapshot() []recordedAdd {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return append([]recordedAdd(nil), fake.adds...)
}

type recordedHeaders struct{ referers, origins []string }

func (fake *fakeQbit) headers() recordedHeaders {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return recordedHeaders{referers: append([]string(nil), fake.referers...), origins: append([]string(nil), fake.origins...)}
}
