package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
)

// ErrLegacyPipelineRows signale une base dont les anciennes tables du pipeline contiennent des lignes : elles ne sont
// jamais supprimées sans l'opérateur.
var ErrLegacyPipelineRows = errors.New("store: legacy pipeline tables contain rows")

// pipelineSchema décrit les tables du pipeline ; les plafonds des BLOB sont aussi garantis par la base.
const pipelineSchema = `
CREATE TABLE IF NOT EXISTS torrent_candidates (
	id INTEGER PRIMARY KEY,
	infohash TEXT NOT NULL,
	tracker TEXT NOT NULL DEFAULT '',
	name TEXT NOT NULL,
	total_size INTEGER NOT NULL,
	provider_json TEXT NOT NULL DEFAULT '{}',
	torrent BLOB CHECK (torrent IS NULL OR length(torrent) <= 8388608),
	state TEXT NOT NULL,
	group_id INTEGER,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (infohash, tracker)
);
CREATE INDEX IF NOT EXISTS torrent_candidates_state ON torrent_candidates(state);
CREATE TABLE IF NOT EXISTS candidate_files (
	id INTEGER PRIMARY KEY,
	candidate_id INTEGER NOT NULL REFERENCES torrent_candidates(id) ON DELETE CASCADE,
	file_index INTEGER NOT NULL,
	path TEXT NOT NULL,
	size INTEGER NOT NULL,
	media_file_id INTEGER REFERENCES media_files(id) ON DELETE SET NULL,
	media_size INTEGER,
	media_mtime INTEGER,
	ambiguous INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS candidate_files_candidate ON candidate_files(candidate_id);
CREATE INDEX IF NOT EXISTS candidate_files_media ON candidate_files(media_file_id);
CREATE TABLE IF NOT EXISTS verifications (
	candidate_id INTEGER PRIMARY KEY REFERENCES torrent_candidates(id) ON DELETE CASCADE,
	total_pieces INTEGER NOT NULL,
	verified_pieces INTEGER NOT NULL,
	checked_bitset BLOB NOT NULL,
	verified_bitset BLOB NOT NULL,
	media_complete INTEGER NOT NULL,
	percent REAL NOT NULL,
	state TEXT NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS verified_files (
	candidate_id INTEGER NOT NULL REFERENCES torrent_candidates(id) ON DELETE CASCADE,
	file_index INTEGER NOT NULL,
	path TEXT NOT NULL,
	size INTEGER NOT NULL,
	sha256 TEXT NOT NULL,
	data BLOB NOT NULL CHECK (length(data) <= 1048576),
	PRIMARY KEY (candidate_id, file_index)
);
CREATE TABLE IF NOT EXISTS proposals (
	id INTEGER PRIMARY KEY,
	candidate_id INTEGER REFERENCES torrent_candidates(id) ON DELETE RESTRICT,
	group_id INTEGER,
	kind TEXT NOT NULL,
	decision TEXT NOT NULL,
	warnings TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	decided_at INTEGER
);
CREATE INDEX IF NOT EXISTS proposals_decision ON proposals(decision);
CREATE TABLE IF NOT EXISTS run_lock (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	holder TEXT NOT NULL,
	stage TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);`

// legacyPipelineTables liste, dans l'ordre de suppression (enfants d'abord), les tables de l'ancienne forme et la
// colonne qui distingue la nouvelle ; une colonne vide signifie que la table n'existe que dans l'ancienne forme.
// restrictCandidates marque une table dont la clé vers torrent_candidates doit être RESTRICT (une proposition
// appliquée est un historique, jamais supprimé en cascade).
var legacyPipelineTables = []struct {
	table, newShapeColumn string
	restrictCandidates    bool
}{
	{"proposals", "group_id", true},
	{"verifications", "verified_bitset", false},
	{"candidates", "", false},
}

// executor exécute et interroge, sur une connexion ou une transaction.
type executor interface {
	queryer
	ExecContext(ctx context.Context, query string, arguments ...any) (sql.Result, error)
}

// upgradeSchema supprime les anciennes tables du pipeline si elles sont vides (refus sinon), crée les tables
// manquantes et ajoute les colonnes manquantes, le tout dans une seule transaction BEGIN IMMEDIATE sur une seule
// connexion : un second processus qui ouvre la base en même temps attend (busy_timeout) puis voit la forme à jour.
func upgradeSchema(database *sql.DB) error {
	ctx := context.Background()
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("lock schema: %w", err)
	}
	if err := upgradeInTransaction(ctx, connection); err != nil {
		_, rollbackErr := connection.ExecContext(ctx, "ROLLBACK")
		return errors.Join(err, rollbackErr)
	}
	_, err = connection.ExecContext(ctx, "COMMIT")
	return err
}

func upgradeInTransaction(ctx context.Context, connection *sql.Conn) error {
	if err := dropLegacyTables(ctx, connection); err != nil {
		return fmt.Errorf("upgrade pipeline tables: %w", err)
	}
	if _, err := connection.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if err := upgradeColumns(connection); err != nil {
		return fmt.Errorf("upgrade schema: %w", err)
	}
	return nil
}

func dropLegacyTables(ctx context.Context, connection *sql.Conn) error {
	var legacyTables []string
	for _, legacy := range legacyPipelineTables {
		isLegacy, err := hasLegacyShape(ctx, connection, legacy.table, legacy.newShapeColumn, legacy.restrictCandidates)
		if err != nil {
			return err
		}
		if isLegacy {
			legacyTables = append(legacyTables, legacy.table)
		}
	}
	for _, table := range legacyTables {
		if err := refuseIfRows(ctx, connection, table); err != nil {
			return err
		}
	}
	for _, table := range legacyTables {
		if _, err := connection.ExecContext(ctx, "DROP TABLE "+table); err != nil { // #nosec G202 -- noms de legacyPipelineTables
			return fmt.Errorf("drop legacy %s: %w", table, err)
		}
	}
	return nil
}

func hasLegacyShape(ctx context.Context, connection *sql.Conn, table, newShapeColumn string, restrictCandidates bool) (bool, error) {
	columns, err := tableColumns(connection, table)
	if err != nil || len(columns) == 0 {
		return false, err
	}
	if newShapeColumn == "" || !slices.Contains(columns, newShapeColumn) {
		return true, nil
	}
	if !restrictCandidates {
		return false, nil
	}
	var cascadingKeys int
	err = connection.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_list(?)
WHERE "table" = 'torrent_candidates' AND on_delete != 'RESTRICT'`, table).Scan(&cascadingKeys)
	return cascadingKeys > 0, err
}

func refuseIfRows(ctx context.Context, connection *sql.Conn, table string) error {
	var rowCount int
	if err := connection.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&rowCount); err != nil { // #nosec G202 -- noms de legacyPipelineTables
		return fmt.Errorf("count legacy %s: %w", table, err)
	}
	if rowCount > 0 {
		return fmt.Errorf("%w: table %s has %d rows, export or empty it before upgrading", ErrLegacyPipelineRows, table, rowCount)
	}
	return nil
}
