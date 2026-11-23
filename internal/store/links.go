package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// LinkState est l'état d'un lien créé par necromanciarr.
type LinkState string

// États d'un lien : ok vérifié, broken cible introuvable, diverged cible changée de contenu, lost lien disparu.
const (
	LinkOK       LinkState = "ok"
	LinkBroken   LinkState = "broken"
	LinkDiverged LinkState = "diverged"
	LinkLost     LinkState = "lost"
)

// Erreurs typées des liens.
var (
	ErrUnknownLinkState = errors.New("store: unknown link state")
	ErrLinkNotFound     = errors.New("store: link not found")
)

// Link est un lien créé, identifié par LinkPath. TargetDevice et TargetInode ne sont conservés que si InodeReliable
// (volume local) : sur un montage FUSE ou réseau, l'inode est synthétique et change au renommage.
type Link struct {
	ID            int64
	Kind          string
	LinkPath      string
	TargetPath    string
	Relative      bool
	TargetDevice  uint64
	TargetInode   uint64
	InodeReliable bool
	Size          int64
	Fingerprint   string
	State         LinkState
	CreatedAt     time.Time
	VerifiedAt    time.Time
	// ModTime est la date de modification de la cible à la création du lien ; zéro si inconnue.
	ModTime time.Time
	// HashAlgo et Hash sont l'empreinte de contenu de la cible fournie par le stockage distant ; vides si inconnue.
	HashAlgo string
	Hash     string
}

func checkLinkState(state LinkState) error {
	switch state {
	case LinkOK, LinkBroken, LinkDiverged, LinkLost:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownLinkState, state)
	}
}

// RecordLink insère le lien, ou remplace toutes ses valeurs si LinkPath est déjà enregistré.
func (openedStore *Store) RecordLink(ctx context.Context, link Link) error {
	if err := checkLinkState(link.State); err != nil {
		return err
	}
	device, inode := nullableIdentity(link)
	_, err := openedStore.database.ExecContext(ctx, `
INSERT INTO links (kind, link_path, target_path, relative, target_dev, target_ino, inode_reliable, size, fingerprint,
	state, created_at, verified_at, mtime, hash_algo, hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(link_path) DO UPDATE SET kind = excluded.kind, target_path = excluded.target_path,
	relative = excluded.relative, target_dev = excluded.target_dev, target_ino = excluded.target_ino,
	inode_reliable = excluded.inode_reliable, size = excluded.size, fingerprint = excluded.fingerprint,
	state = excluded.state, created_at = excluded.created_at, verified_at = excluded.verified_at, mtime = excluded.mtime,
	hash_algo = excluded.hash_algo, hash = excluded.hash`,
		link.Kind, link.LinkPath, link.TargetPath, link.Relative, device, inode, link.InodeReliable, link.Size,
		link.Fingerprint, string(link.State), link.CreatedAt.UnixNano(), link.VerifiedAt.UnixNano(), nullableModTime(link.ModTime),
		nullableText(link.HashAlgo), nullableText(link.Hash))
	if err != nil {
		return fmt.Errorf("record link: %w", err)
	}
	return nil
}

// nullableModTime stocke NULL pour une date inconnue (zéro) plutôt qu'une date de l'an 1.
func nullableModTime(modTime time.Time) sql.NullInt64 {
	return sql.NullInt64{Int64: modTime.UnixNano(), Valid: !modTime.IsZero()}
}

// nullableText stocke NULL pour une chaîne vide.
func nullableText(text string) sql.NullString {
	return sql.NullString{String: text, Valid: text != ""}
}

// nullableIdentity : SQLite ne stocke que des entiers signés ; le motif binaire de l'uint64 est conservé tel quel.
func nullableIdentity(link Link) (sql.NullInt64, sql.NullInt64) {
	if !link.InodeReliable {
		return sql.NullInt64{}, sql.NullInt64{}
	}
	device := sql.NullInt64{Int64: int64(link.TargetDevice), Valid: true} // #nosec G115 -- motif binaire conservé, relu en uint64
	inode := sql.NullInt64{Int64: int64(link.TargetInode), Valid: true}   // #nosec G115 -- motif binaire conservé, relu en uint64
	return device, inode
}

// ListLinks renvoie les liens dans l'état demandé (tous si state est vide), triés par LinkPath ; dates en UTC.
func (openedStore *Store) ListLinks(ctx context.Context, state LinkState) ([]Link, error) {
	rows, err := openedStore.database.QueryContext(ctx, `
SELECT id, kind, link_path, target_path, relative, target_dev, target_ino, inode_reliable, size, fingerprint, state,
	created_at, verified_at, mtime, hash_algo, hash FROM links WHERE ? = '' OR state = ? ORDER BY link_path`, string(state), string(state))
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var links []Link
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func scanLink(rows *sql.Rows) (Link, error) {
	var link Link
	var device, inode, modTimeNanoseconds sql.NullInt64
	var hashAlgo, hash sql.NullString
	var state string
	var createdAtNanoseconds, verifiedAtNanoseconds int64
	err := rows.Scan(&link.ID, &link.Kind, &link.LinkPath, &link.TargetPath, &link.Relative, &device, &inode,
		&link.InodeReliable, &link.Size, &link.Fingerprint, &state, &createdAtNanoseconds, &verifiedAtNanoseconds, &modTimeNanoseconds, &hashAlgo, &hash)
	if err != nil {
		return Link{}, fmt.Errorf("scan link: %w", err)
	}
	link.TargetDevice = uint64(device.Int64) // #nosec G115 -- motif binaire écrit par nullableIdentity
	link.TargetInode = uint64(inode.Int64)   // #nosec G115 -- motif binaire écrit par nullableIdentity
	link.HashAlgo, link.Hash = hashAlgo.String, hash.String
	link.State = LinkState(state)
	link.CreatedAt = time.Unix(0, createdAtNanoseconds).UTC()
	link.VerifiedAt = time.Unix(0, verifiedAtNanoseconds).UTC()
	if modTimeNanoseconds.Valid {
		link.ModTime = time.Unix(0, modTimeNanoseconds.Int64).UTC()
	}
	return link, nil
}

// UpdateLinkTarget change la cible enregistrée du lien linkPath, après un renommage dans la bibliothèque.
func (openedStore *Store) UpdateLinkTarget(ctx context.Context, linkPath, targetPath string) error {
	return openedStore.updateLink(ctx, `UPDATE links SET target_path = ? WHERE link_path = ?`, targetPath, linkPath)
}

// SetLinkState enregistre l'état du lien linkPath et la date de sa vérification.
func (openedStore *Store) SetLinkState(ctx context.Context, linkPath string, state LinkState, verifiedAt time.Time) error {
	if err := checkLinkState(state); err != nil {
		return err
	}
	return openedStore.updateLink(ctx, `UPDATE links SET state = ?, verified_at = ? WHERE link_path = ?`,
		string(state), verifiedAt.UnixNano(), linkPath)
}

func (openedStore *Store) updateLink(ctx context.Context, query string, arguments ...any) error {
	outcome, err := openedStore.database.ExecContext(ctx, query, arguments...)
	if err != nil {
		return fmt.Errorf("update link: %w", err)
	}
	affectedRows, err := outcome.RowsAffected()
	if err != nil {
		return fmt.Errorf("update link: %w", err)
	}
	if affectedRows == 0 {
		return ErrLinkNotFound
	}
	return nil
}

// CachedFingerprint est l'empreinte début et fin d'un fichier, valable tant que sa taille et sa date sont inchangées.
type CachedFingerprint struct {
	Path         string
	Size         int64
	ModTime      time.Time
	HeadTailHash string
	VerifiedAt   time.Time
	// HashAlgo et Hash : empreinte de contenu fournie par le stockage distant, vides si inconnue.
	HashAlgo string
	Hash     string
}

// RecordFingerprint enregistre l'empreinte, en remplaçant celle déjà connue pour le même chemin.
func (openedStore *Store) RecordFingerprint(ctx context.Context, entry CachedFingerprint) error {
	_, err := openedStore.database.ExecContext(ctx, `
INSERT INTO fingerprints (path, size, mtime, head_tail_hash, verified_at, hash_algo, hash) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime,
	head_tail_hash = excluded.head_tail_hash, verified_at = excluded.verified_at,
	hash_algo = excluded.hash_algo, hash = excluded.hash`,
		entry.Path, entry.Size, entry.ModTime.UnixNano(), entry.HeadTailHash, entry.VerifiedAt.UnixNano(),
		nullableText(entry.HashAlgo), nullableText(entry.Hash))
	if err != nil {
		return fmt.Errorf("record fingerprint: %w", err)
	}
	return nil
}

// LookupFingerprint renvoie l'empreinte de path si elle a été calculée pour exactement cette taille et cette date.
func (openedStore *Store) LookupFingerprint(ctx context.Context, path string, size int64, modTime time.Time) (string, bool, error) {
	var headTailHash string
	err := openedStore.database.QueryRowContext(ctx,
		`SELECT head_tail_hash FROM fingerprints WHERE path = ? AND size = ? AND mtime = ?`,
		path, size, modTime.UnixNano()).Scan(&headTailHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup fingerprint: %w", err)
	}
	return headTailHash, true, nil
}
