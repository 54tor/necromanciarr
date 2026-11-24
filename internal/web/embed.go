package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Fichiers de l'interface : index.html sert toutes les routes du client ; assets/ contient les fichiers nommés par
// leur empreinte (Vite), donc immuables.
const (
	indexFile        = "index.html"
	hashedAssetsDir  = "assets/"
	immutableCaching = "public, max-age=31536000, immutable"
)

// notBuiltPage est servie quand l'interface n'a pas été construite (seul le marqueur .placeholder est embarqué) :
// aucune ressource externe, aucun script ni style, compatible avec la CSP.
const notBuiltPage = `<!doctype html>
<html lang="fr">
<head><meta charset="utf-8"><title>Necromanciarr : interface non construite</title></head>
<body>
<h1>Interface non construite</h1>
<p>Ce binaire a été compilé sans l'interface web. Lancez <code>make ui</code> puis recompilez.
L'API reste disponible sous /api.</p>
</body>
</html>
`

// assetsHandler sert assets avec repli sur index.html pour les routes du client (chemin sans extension). Un fichier
// avec extension absent répond 404. Les noms passent par fs.ValidPath : aucun chemin ne sort de assets.
func assetsHandler(assets fs.FS) http.Handler {
	index, err := fs.ReadFile(assets, indexFile)
	if err != nil {
		index = []byte(notBuiltPage)
	}
	isBuilt := err == nil
	fileServer := http.FileServerFS(assets)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", "GET, HEAD")
			http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+request.URL.Path), "/")
		isFile := isBuilt && isRegularFile(assets, name) && name != indexFile
		switch {
		case isFile:
			if strings.HasPrefix(name, hashedAssetsDir) {
				writer.Header().Set("Cache-Control", immutableCaching)
			}
			fileServer.ServeHTTP(writer, request)
		case path.Ext(name) != "" && name != indexFile:
			http.NotFound(writer, request)
		default:
			serveIndex(writer, request, index)
		}
	})
}

func isRegularFile(assets fs.FS, name string) bool {
	if !fs.ValidPath(name) || name == "." {
		return false
	}
	info, err := fs.Stat(assets, name)
	return err == nil && info.Mode().IsRegular()
}

// serveIndex sert la page sans cache : elle référence les fichiers hachés du build courant.
func serveIndex(writer http.ResponseWriter, request *http.Request, index []byte) {
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(writer, request, indexFile, time.Time{}, bytes.NewReader(index))
}
