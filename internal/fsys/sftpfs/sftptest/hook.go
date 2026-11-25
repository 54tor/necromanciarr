package sftptest

import (
	"os"
	"path/filepath"
	"testing"
)

// OnRequest appelle callback pour chaque requête de cette méthode (nom de Request.Method), après son
// enregistrement et avant qu'elle soit servie : un acteur hostile qui modifie l'arbre entre deux requêtes du client,
// de façon déterministe. callback tourne dans une goroutine du serveur : t.Fatal y est interdit.
func OnRequest(method string, callback func(Request)) Option {
	return func(current *settings) {
		current.requestHooks[method] = append(current.requestHooks[method], callback)
	}
}

// WithDirectoryMode donne à chaque dossier créé par Mkdir exactement ce mode, comme un compte au umask permissif
// (0o777 : dossiers modifiables par tous).
func WithDirectoryMode(mode os.FileMode) Option {
	return func(current *settings) { current.directoryMode = mode }
}

func (server *Server) runHooks(request Request) {
	for _, callback := range server.settings.requestHooks[request.Method] {
		callback(request)
	}
}

// Mkdir crée un dossier de test (et ses parents) sous le dossier servi, chemin local ; t.Fatal en cas d'échec.
func Mkdir(t testing.TB, localPath string) {
	t.Helper()
	if err := os.MkdirAll(localPath, fixtureDirectoryMode); err != nil {
		t.Fatal(err)
	}
}

// ReplaceWithSymlink remplace l'entrée localPath (fichier, lien ou dossier, contenu compris) par un lien symbolique
// vers target, texte brut : le piège posé par un acteur qui écrit dans le dossier de liens. Utilisable depuis
// OnRequest : un échec est signalé par t.Errorf, jamais t.Fatal.
func ReplaceWithSymlink(t testing.TB, localPath, target string) {
	t.Helper()
	if err := os.RemoveAll(localPath); err != nil {
		t.Errorf("replace %s: %v", filepath.Base(localPath), err)
		return
	}
	if err := os.Symlink(target, localPath); err != nil {
		t.Errorf("symlink %s: %v", filepath.Base(localPath), err)
	}
}
