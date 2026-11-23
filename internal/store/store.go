package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS media_files (
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	tmdb_id INTEGER NOT NULL DEFAULT 0,
	tvdb_id INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS candidates (
	id INTEGER PRIMARY KEY,
	media_file_id INTEGER NOT NULL REFERENCES media_files(id),
	tracker TEXT NOT NULL,
	infohash TEXT NOT NULL,
	state TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS verifications (
	id INTEGER PRIMARY KEY,
	candidate_id INTEGER NOT NULL REFERENCES candidates(id),
	verified_pieces INTEGER NOT NULL,
	total_pieces INTEGER NOT NULL,
	missing_pieces_json TEXT NOT NULL,
	state TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS proposals (
	id INTEGER PRIMARY KEY,
	candidate_id INTEGER NOT NULL REFERENCES candidates(id),
	kind TEXT NOT NULL,
	decision TEXT NOT NULL
);`

type MediaFile struct {
	ID      int64
	Path    string
	Size    int64
	ModTime time.Time
	TMDBID  int
	TVDBID  int
}

type Store struct {
	database *sql.DB
}

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
	if _, err := database.Exec(schema); err != nil {
		return nil, errors.Join(fmt.Errorf("create schema: %w", err), database.Close())
	}
	return &Store{database: database}, nil
}

func (openedStore *Store) Close() error {
	return openedStore.database.Close()
}

func (openedStore *Store) UpsertMediaFile(ctx context.Context, file MediaFile) error {
	_, err := openedStore.database.ExecContext(ctx, `
INSERT INTO media_files (path, size, mtime, tmdb_id, tvdb_id) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime,
	tmdb_id = excluded.tmdb_id, tvdb_id = excluded.tvdb_id`,
		file.Path, file.Size, file.ModTime.UnixNano(), file.TMDBID, file.TVDBID)
	if err != nil {
		return fmt.Errorf("upsert media file: %w", err)
	}
	return nil
}

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
