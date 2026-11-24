// Package store persiste l'état du pipeline dans une base SQLite sans CGO.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"time"

	_ "modernc.org/sqlite"
)

// schema est rejouable : chaque table est créée seulement si elle manque, donc Open est idempotent.
const schema = `
CREATE TABLE IF NOT EXISTS media_files (
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	tmdb_id INTEGER NOT NULL DEFAULT 0,
	tvdb_id INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS links (
	id INTEGER PRIMARY KEY,
	kind TEXT NOT NULL,
	link_path TEXT NOT NULL UNIQUE,
	target_path TEXT NOT NULL,
	relative INTEGER NOT NULL,
	target_dev INTEGER,
	target_ino INTEGER,
	inode_reliable INTEGER NOT NULL,
	size INTEGER NOT NULL,
	fingerprint TEXT NOT NULL,
	state TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	verified_at INTEGER NOT NULL,
	mtime INTEGER,
	hash_algo TEXT,
	hash TEXT
);
CREATE TABLE IF NOT EXISTS fingerprints (
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	head_tail_hash TEXT NOT NULL,
	verified_at INTEGER NOT NULL,
	hash_algo TEXT,
	hash TEXT
);
CREATE TABLE IF NOT EXISTS mount_capabilities (
	id INTEGER PRIMARY KEY,
	mount_path TEXT NOT NULL UNIQUE,
	hashes TEXT NOT NULL,
	modtime INTEGER NOT NULL,
	symlinks INTEGER NOT NULL,
	probed_at INTEGER NOT NULL
);` + pipelineSchema

// MediaFile est un fichier média de la bibliothèque, identifié par son chemin ; TMDBID et TVDBID valent 0 si inconnus.
type MediaFile struct {
	ID      int64
	Path    string
	Size    int64
	ModTime time.Time
	TMDBID  int
	TVDBID  int
}

// Store donne accès à la base SQLite ; sûr pour un usage concurrent via database/sql.
type Store struct {
	database *sql.DB
}

// pragmaQuery attend 5 s sur un verrou au lieu d'échouer, et active les clés étrangères (désactivées par défaut dans SQLite).
const pragmaQuery = "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

// dataSourceName construit une URI SQLite : `?`, `#` et `%` du chemin sont échappés,
// et la lettre de lecteur Windows reste dans le chemin (`file:C:/x/y.db`).
func dataSourceName(path string) string {
	sourceURL := url.URL{
		Scheme:   "file",
		OmitHost: true,
		Path:     filepath.ToSlash(path),
		RawQuery: pragmaQuery,
	}
	return sourceURL.String()
}

// Open ouvre (et crée au besoin) la base SQLite située à path.
func Open(path string) (*Store, error) {
	database, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("open store: %w", err)
	}
	if err := upgradeSchema(database); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	return &Store{database: database}, nil
}

// Close ferme la base.
func (openedStore *Store) Close() error {
	return openedStore.database.Close()
}

// UpsertMediaFile insère le fichier, ou met à jour taille et date si son chemin existe déjà ; un identifiant externe
// à 0 garde la valeur connue (le parcours de la bibliothèque n'en fournit pas), SetMediaFileIDs les remplace.
func (openedStore *Store) UpsertMediaFile(ctx context.Context, file MediaFile) error {
	_, err := openedStore.database.ExecContext(ctx, `
INSERT INTO media_files (path, size, mtime, tmdb_id, tvdb_id) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime,
	tmdb_id = CASE WHEN excluded.tmdb_id != 0 THEN excluded.tmdb_id ELSE tmdb_id END,
	tvdb_id = CASE WHEN excluded.tvdb_id != 0 THEN excluded.tvdb_id ELSE tvdb_id END`,
		file.Path, file.Size, file.ModTime.UnixNano(), file.TMDBID, file.TVDBID)
	if err != nil {
		return fmt.Errorf("upsert media file: %w", err)
	}
	return nil
}

// ListMediaFiles renvoie tous les fichiers triés par chemin ; ModTime est en UTC.
func (openedStore *Store) ListMediaFiles(ctx context.Context) ([]MediaFile, error) {
	rows, err := openedStore.database.QueryContext(ctx,
		`SELECT id, path, size, mtime, tmdb_id, tvdb_id FROM media_files ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("list media files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var mediaFiles []MediaFile
	for rows.Next() {
		var file MediaFile
		var modTimeNanoseconds int64
		if err := rows.Scan(&file.ID, &file.Path, &file.Size, &modTimeNanoseconds, &file.TMDBID, &file.TVDBID); err != nil {
			return nil, fmt.Errorf("scan media file: %w", err)
		}
		file.ModTime = time.Unix(0, modTimeNanoseconds).UTC()
		mediaFiles = append(mediaFiles, file)
	}
	return mediaFiles, rows.Err()
}

// columnUpgrades liste les colonnes ajoutées après la première version du schéma ; les bases plus anciennes les
// reçoivent à l'ouverture.
var columnUpgrades = []struct{ table, column, definition string }{
	{"links", "mtime", "INTEGER"},
	{"links", "hash_algo", "TEXT"},
	{"links", "hash", "TEXT"},
	{"fingerprints", "hash_algo", "TEXT"},
	{"fingerprints", "hash", "TEXT"},
	{"mount_capabilities", "provider", "TEXT NOT NULL DEFAULT ''"},
}

func upgradeColumns(database executor) error {
	for _, upgrade := range columnUpgrades {
		if err := addColumnIfMissing(database, upgrade.table, upgrade.column, upgrade.definition); err != nil {
			return fmt.Errorf("%s.%s: %w", upgrade.table, upgrade.column, err)
		}
	}
	return nil
}

// addColumnIfMissing ajoute la colonne si pragma_table_info ne la voit pas ; sans effet sinon, donc rejouable.
// Table, colonne et définition viennent de columnUpgrades, jamais d'une entrée externe.
func addColumnIfMissing(database executor, table, column, definition string) error {
	present, err := hasColumn(database, table, column)
	if err != nil || present {
		return err
	}
	_, err = database.ExecContext(context.Background(), "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition) // #nosec G202 -- constantes de columnUpgrades
	return err
}

func hasColumn(database queryer, table, column string) (bool, error) {
	columns, err := tableColumns(database, table)
	return slices.Contains(columns, column), err
}

// tableColumns renvoie les colonnes de la table, vide si elle n'existe pas.
func tableColumns(database queryer, table string) ([]string, error) {
	rows, err := database.QueryContext(context.Background(), `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, err
		}
		columns = append(columns, name)
	}
	return columns, errors.Join(rows.Err(), rows.Close())
}
