package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrAccountExists : le compte opérateur existe déjà ; la base n'en accepte qu'un.
var ErrAccountExists = errors.New("store: account already exists")

// ErrAccountChanged : le compte n'est plus celui contre lequel la preuve a été vérifiée (remis à zéro, recréé ou
// mot de passe changé) ; aucune session n'est enregistrée.
var ErrAccountChanged = errors.New("store: account changed since the proof was checked")

// Account est le compte opérateur ; PasswordHash est l'empreinte du mot de passe, jamais le mot de passe.
type Account struct {
	Username, PasswordHash string
	CreatedAt              time.Time
}

// WebAuthnCredential est une clé de sécurité ; Kind vaut second_factor ou passkey, Record est le JSON de la
// credential WebAuthn, opaque pour le store.
type WebAuthnCredential struct {
	ID         []byte
	Name, Kind string
	Record     []byte
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// TOTPFactor est le secret TOTP scellé et le dernier pas accepté.
type TOTPFactor struct {
	SecretSealed []byte
	LastStep     int64
	CreatedAt    time.Time
}

// SessionRecord est une session ouverte ; ID est l'empreinte hexadécimale SHA-256 du jeton, le store ne voit jamais
// le jeton. AccountCreatedAt est la génération du compte pour laquelle la session a été émise ; une session d'avant
// cette colonne porte l'instant zéro Unix.
type SessionRecord struct {
	ID                               string
	CreatedAt, LastSeenAt, ExpiresAt time.Time
	Address, UserAgent               string
	AccountCreatedAt                 time.Time
}

// CreateAccount enregistre le compte ; ErrAccountExists s'il y en a déjà un, y compris sous appels concurrents.
func (openedStore *Store) CreateAccount(ctx context.Context, account Account) error {
	result, err := openedStore.database.ExecContext(ctx,
		`INSERT INTO account (id, username, password_hash, created_at) VALUES (1, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		account.Username, account.PasswordHash, account.CreatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create account: %w", err)
	}
	if inserted == 0 {
		return ErrAccountExists
	}
	return nil
}

// Account renvoie le compte ; le booléen est faux s'il n'existe pas encore.
func (openedStore *Store) Account(ctx context.Context) (Account, bool, error) {
	var account Account
	var createdAt int64
	err := openedStore.database.QueryRowContext(ctx,
		`SELECT username, password_hash, created_at FROM account WHERE id = 1`).Scan(&account.Username, &account.PasswordHash, &createdAt)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, fmt.Errorf("read account: %w", err)
	}
	account.CreatedAt = time.Unix(0, createdAt).UTC()
	return account, true, nil
}

// ReplacePasswordHash remplace l'empreinte seulement si elle vaut encore previousHash ; faux si elle a changé entre-temps.
func (openedStore *Store) ReplacePasswordHash(ctx context.Context, previousHash, newHash string) (bool, error) {
	result, err := openedStore.database.ExecContext(ctx,
		`UPDATE account SET password_hash = ? WHERE id = 1 AND password_hash = ?`, newHash, previousHash)
	if err != nil {
		return false, fmt.Errorf("replace password hash: %w", err)
	}
	replaced, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("replace password hash: %w", err)
	}
	return replaced == 1, nil
}

// ChangePasswordHash remplace l'empreinte du mot de passe et ferme toutes les sessions sauf keepSessionID, dans une
// seule transaction.
func (openedStore *Store) ChangePasswordHash(ctx context.Context, newHash, keepSessionID string) error {
	transaction, err := openedStore.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `UPDATE account SET password_hash = ? WHERE id = 1`, newHash); err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE id <> ?`, keepSessionID); err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	return nil
}

var resetAccountStatements = []string{
	`DELETE FROM sessions`,
	`DELETE FROM totp_factor`,
	`DELETE FROM webauthn_credentials`,
	`DELETE FROM account`,
}

// ResetAccount efface le compte, ses clés, son TOTP et ses sessions dans une seule transaction.
func (openedStore *Store) ResetAccount(ctx context.Context) error {
	transaction, err := openedStore.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reset account: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	for _, statement := range resetAccountStatements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("reset account: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("reset account: %w", err)
	}
	return nil
}

// AddCredential enregistre une clé de sécurité.
func (openedStore *Store) AddCredential(ctx context.Context, credential WebAuthnCredential) error {
	_, err := openedStore.database.ExecContext(ctx,
		`INSERT INTO webauthn_credentials (id, name, kind, record, created_at, last_used_at) VALUES (?, ?, ?, ?, ?, ?)`,
		credential.ID, credential.Name, credential.Kind, credential.Record, credential.CreatedAt.UnixNano(),
		nullableUnixNano(credential.LastUsedAt))
	if err != nil {
		return fmt.Errorf("add credential: %w", err)
	}
	return nil
}

func nullableUnixNano(moment *time.Time) any {
	if moment == nil {
		return nil
	}
	return moment.UnixNano()
}

// Credentials renvoie les clés de sécurité, de la plus ancienne à la plus récente.
func (openedStore *Store) Credentials(ctx context.Context) ([]WebAuthnCredential, error) {
	rows, err := openedStore.database.QueryContext(ctx,
		`SELECT id, name, kind, record, created_at, last_used_at FROM webauthn_credentials ORDER BY created_at, rowid`)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var credentials []WebAuthnCredential
	for rows.Next() {
		var credential WebAuthnCredential
		var createdAt int64
		var lastUsedAt sql.NullInt64
		if err := rows.Scan(&credential.ID, &credential.Name, &credential.Kind, &credential.Record, &createdAt, &lastUsedAt); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		credential.CreatedAt = time.Unix(0, createdAt).UTC()
		if lastUsedAt.Valid {
			usedAt := time.Unix(0, lastUsedAt.Int64).UTC()
			credential.LastUsedAt = &usedAt
		}
		credentials = append(credentials, credential)
	}
	return credentials, rows.Err()
}

// UpdateCredential remplace l'enregistrement d'une clé (compteur de signatures) et date son dernier usage.
func (openedStore *Store) UpdateCredential(ctx context.Context, id, record []byte, usedAt time.Time) error {
	if _, err := openedStore.database.ExecContext(ctx,
		`UPDATE webauthn_credentials SET record = ?, last_used_at = ? WHERE id = ?`, record, usedAt.UnixNano(), id); err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	return nil
}

// RenameCredential change le nom affiché d'une clé.
func (openedStore *Store) RenameCredential(ctx context.Context, id []byte, name string) error {
	if _, err := openedStore.database.ExecContext(ctx, `UPDATE webauthn_credentials SET name = ? WHERE id = ?`, name, id); err != nil {
		return fmt.Errorf("rename credential: %w", err)
	}
	return nil
}

// DeleteCredential supprime une clé ; sans effet si elle n'existe pas.
func (openedStore *Store) DeleteCredential(ctx context.Context, id []byte) error {
	if _, err := openedStore.database.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}

// CreateSession enregistre la session seulement si le compte est encore provenAgainst (même génération, même
// empreinte de mot de passe), en une instruction ; ErrAccountChanged sinon.
func (openedStore *Store) CreateSession(ctx context.Context, session SessionRecord, provenAgainst Account) error {
	result, err := openedStore.database.ExecContext(ctx,
		`INSERT INTO sessions (id, created_at, last_seen_at, expires_at, address, user_agent, account_created_at)
		SELECT ?, ?, ?, ?, ?, ?, created_at FROM account WHERE id = 1 AND created_at = ? AND password_hash = ?`,
		session.ID, session.CreatedAt.UnixNano(), session.LastSeenAt.UnixNano(), session.ExpiresAt.UnixNano(),
		session.Address, session.UserAgent, provenAgainst.CreatedAt.UnixNano(), provenAgainst.PasswordHash)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	if inserted == 0 {
		return ErrAccountChanged
	}
	return nil
}

const sessionColumns = `id, created_at, last_seen_at, expires_at, address, user_agent, account_created_at`

type rowScanner interface {
	Scan(destinations ...any) error
}

func scanSession(row rowScanner) (SessionRecord, error) {
	var session SessionRecord
	var createdAt, lastSeenAt, expiresAt, accountCreatedAt int64
	if err := row.Scan(&session.ID, &createdAt, &lastSeenAt, &expiresAt, &session.Address, &session.UserAgent, &accountCreatedAt); err != nil {
		return SessionRecord{}, err
	}
	session.AccountCreatedAt = time.Unix(0, accountCreatedAt).UTC()
	session.CreatedAt = time.Unix(0, createdAt).UTC()
	session.LastSeenAt = time.Unix(0, lastSeenAt).UTC()
	session.ExpiresAt = time.Unix(0, expiresAt).UTC()
	return session, nil
}

// Session renvoie la session ; le booléen est faux si elle n'existe pas.
func (openedStore *Store) Session(ctx context.Context, id string) (SessionRecord, bool, error) {
	session, err := scanSession(openedStore.database.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return SessionRecord{}, false, nil
	}
	if err != nil {
		return SessionRecord{}, false, fmt.Errorf("read session: %w", err)
	}
	return session, true, nil
}

// TouchSession date le dernier passage de la session.
func (openedStore *Store) TouchSession(ctx context.Context, id string, seenAt time.Time) error {
	if _, err := openedStore.database.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id = ?`, seenAt.UnixNano(), id); err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Sessions renvoie les sessions, de la plus récente à la plus ancienne.
func (openedStore *Store) Sessions(ctx context.Context) ([]SessionRecord, error) {
	rows, err := openedStore.database.QueryContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions ORDER BY created_at DESC, rowid DESC`)
	if isMissingTable(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []SessionRecord
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// DeleteSession ferme la session ; sans effet si elle n'existe pas.
func (openedStore *Store) DeleteSession(ctx context.Context, id string) error {
	if _, err := openedStore.database.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteAllSessions ferme toutes les sessions.
func (openedStore *Store) DeleteAllSessions(ctx context.Context) error {
	if _, err := openedStore.database.ExecContext(ctx, `DELETE FROM sessions`); err != nil {
		return fmt.Errorf("delete all sessions: %w", err)
	}
	return nil
}

// PurgeExpiredSessions supprime les sessions expirées à now et renvoie leur nombre.
func (openedStore *Store) PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	result, err := openedStore.database.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UnixNano())
	if err != nil {
		return 0, fmt.Errorf("purge expired sessions: %w", err)
	}
	purged, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge expired sessions: %w", err)
	}
	return purged, nil
}

// SetTOTP enregistre ou remplace le facteur TOTP.
func (openedStore *Store) SetTOTP(ctx context.Context, factor TOTPFactor) error {
	_, err := openedStore.database.ExecContext(ctx,
		`INSERT INTO totp_factor (id, secret_sealed, last_step, created_at) VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET secret_sealed = excluded.secret_sealed, last_step = excluded.last_step, created_at = excluded.created_at`,
		factor.SecretSealed, factor.LastStep, factor.CreatedAt.UnixNano())
	if err != nil {
		return fmt.Errorf("set totp: %w", err)
	}
	return nil
}

// TOTP renvoie le facteur TOTP ; le booléen est faux s'il n'est pas configuré.
func (openedStore *Store) TOTP(ctx context.Context) (TOTPFactor, bool, error) {
	var factor TOTPFactor
	var createdAt int64
	err := openedStore.database.QueryRowContext(ctx,
		`SELECT secret_sealed, last_step, created_at FROM totp_factor WHERE id = 1`).Scan(&factor.SecretSealed, &factor.LastStep, &createdAt)
	if errors.Is(err, sql.ErrNoRows) || isMissingTable(err) {
		return TOTPFactor{}, false, nil
	}
	if err != nil {
		return TOTPFactor{}, false, fmt.Errorf("read totp: %w", err)
	}
	factor.CreatedAt = time.Unix(0, createdAt).UTC()
	return factor, true, nil
}

// DeleteTOTP supprime le facteur TOTP.
func (openedStore *Store) DeleteTOTP(ctx context.Context) error {
	if _, err := openedStore.database.ExecContext(ctx, `DELETE FROM totp_factor`); err != nil {
		return fmt.Errorf("delete totp: %w", err)
	}
	return nil
}

// AdvanceTOTPStep accepte le pas une seule fois : vrai seulement si step dépasse le dernier pas et si le secret scellé
// est encore celui que l'appelant a vérifié, en une instruction atomique, donc un code rejoué ou issu d'un secret
// remplacé entre-temps est refusé même sous appels concurrents.
func (openedStore *Store) AdvanceTOTPStep(ctx context.Context, sealedSecret []byte, step int64) (bool, error) {
	result, err := openedStore.database.ExecContext(ctx,
		`UPDATE totp_factor SET last_step = ? WHERE id = 1 AND secret_sealed = ? AND last_step < ?`, step, sealedSecret, step)
	if err != nil {
		return false, fmt.Errorf("advance totp step: %w", err)
	}
	advanced, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("advance totp step: %w", err)
	}
	return advanced == 1, nil
}
