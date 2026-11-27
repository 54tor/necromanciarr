package store

// accountSchema décrit le compte opérateur : une seule ligne (id = 1) pour le compte et pour le facteur TOTP, ce qui
// rend un second compte impossible au niveau de la base.
const accountSchema = `
CREATE TABLE IF NOT EXISTS account (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	username TEXT NOT NULL,
	password_hash TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS webauthn_credentials (
	id BLOB PRIMARY KEY,
	name TEXT NOT NULL,
	kind TEXT NOT NULL CHECK (kind IN ('second_factor', 'passkey')),
	record BLOB NOT NULL,
	created_at INTEGER NOT NULL,
	last_used_at INTEGER
);
CREATE TABLE IF NOT EXISTS totp_factor (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	secret_sealed BLOB NOT NULL,
	last_step INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	address TEXT NOT NULL,
	user_agent TEXT NOT NULL,
	account_created_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS sessions_expiry ON sessions(expires_at);`
