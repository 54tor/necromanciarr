package web

import (
	"bytes"
	"html"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	"necromanciarr/internal/i18n"
)

// Fichiers de l'interface : index.html sert toutes les routes du client ; assets/ contient les fichiers nommés par
// leur empreinte (Vite), donc immuables.
const (
	indexFile        = "index.html"
	hashedAssetsDir  = "assets/"
	immutableCaching = "public, max-age=31536000, immutable"
	uiBuildCommand   = "make ui"
)

// notBuiltPage rend la page servie quand l'interface n'a pas été construite (seul le marqueur .placeholder est
// embarqué) : texte dans la langue configurée, aucune ressource externe, aucun script ni style, compatible avec la CSP.
func notBuiltPage(translator i18n.Translator) string {
	language := html.EscapeString(string(translator.Language()))
	title := html.EscapeString(translator.Text(i18n.KeyWebUINotBuiltTitle, nil))
	heading := html.EscapeString(translator.Text(i18n.KeyWebUINotBuiltHeading, nil))
	body := html.EscapeString(translator.Text(i18n.KeyWebUINotBuiltBody, map[string]string{"command": uiBuildCommand}))
	body = strings.Replace(body, uiBuildCommand, "<code>"+uiBuildCommand+"</code>", 1)
	return "<!doctype html>\n<html lang=\"" + language + "\">\n<head><meta charset=\"utf-8\"><title>" + title +
		"</title></head>\n<body>\n<h1>" + heading + "</h1>\n<p>" + body + "</p>\n</body>\n</html>\n"
}

// assetsHandler sert assets avec repli sur index.html pour les routes du client (chemin sans extension). Un fichier
// avec extension absent répond 404. Les noms passent par fs.ValidPath : aucun chemin ne sort de assets.
func assetsHandler(assets fs.FS, translator i18n.Translator) http.Handler {
	index, err := fs.ReadFile(assets, indexFile)
	if err != nil {
		index = []byte(notBuiltPage(translator))
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
