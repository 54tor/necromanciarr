package app

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"necromanciarr/internal/auth"
	"necromanciarr/internal/store"
)

// Facteurs que Login peut demander après un mot de passe correct.
const (
	SecondFactorWebAuthn = "webauthn"
	SecondFactorTOTP     = "totp"
)

const (
	// loginThrottleKey : un seul compte, donc une seule clé, jamais tirée de l'identifiant saisi.
	loginThrottleKey           = "account"
	maxLabelRunes              = 64
	maxClientAddressBytes      = 128
	maxClientUserAgentBytes    = 256
	maxConcurrentArgon         = 4
	credentialKindSecondFactor = "second_factor"
	credentialKindPasskey      = "passkey"
)

var (
	// ErrAccountExists : le compte opérateur existe déjà (même valeur que celle du store).
	ErrAccountExists = store.ErrAccountExists
	// ErrInvalidCredentials : identifiant inconnu ou mot de passe faux, sans distinction.
	ErrInvalidCredentials = errors.New("app: invalid username or password")
	// ErrSessionInvalid : session inconnue, expirée ou sans compte.
	ErrSessionInvalid = errors.New("app: session invalid or expired")
	// ErrThrottled : trop d'essais ; ThrottledError porte le délai.
	ErrThrottled = errors.New("app: too many attempts, retry later")
	// ErrInvalidUsername : identifiant vide, trop long ou avec un caractère de contrôle.
	ErrInvalidUsername = errors.New("app: invalid username")
)

// argonSlots borne les dérivations argon2 simultanées du processus (64 MiB chacun).
var argonSlots = make(chan struct{}, maxConcurrentArgon)

// ThrottledError refuse un essai pendant le délai du limiteur ; errors.Is(err, ErrThrottled) est vrai.
type ThrottledError struct{ RetryAfter time.Duration }

func (throttled *ThrottledError) Error() string {
	return fmt.Sprintf("%s (retry after %s)", ErrThrottled, throttled.RetryAfter)
}

func (throttled *ThrottledError) Is(target error) bool { return target == ErrThrottled }

// AuthDeps remplace, dans les tests, le limiteur et la vérification factice ; nil vaut la valeur de production.
type AuthDeps struct {
	Throttle    *auth.Throttle
	VerifyDummy func(password string)
}

// AccountStore est la part du store qui garde le compte, ses facteurs et ses sessions.
type AccountStore interface {
	CreateAccount(ctx context.Context, account store.Account) error
	Account(ctx context.Context) (store.Account, bool, error)
	ReplacePasswordHash(ctx context.Context, previousHash, newHash string) (bool, error)
	ChangePasswordHash(ctx context.Context, verified store.Account, newHash, keepSessionID string) error
	ResetAccount(ctx context.Context) error
	Credentials(ctx context.Context) ([]store.WebAuthnCredential, error)
	AddCredential(ctx context.Context, credential store.WebAuthnCredential) error
	UpdateCredential(ctx context.Context, id, record []byte, usedAt time.Time) error
	RenameCredential(ctx context.Context, id []byte, name string) error
	DeleteCredential(ctx context.Context, id []byte) error
	TOTP(ctx context.Context) (store.TOTPFactor, bool, error)
	SetTOTP(ctx context.Context, factor store.TOTPFactor) error
	DeleteTOTP(ctx context.Context) error
	AdvanceTOTPStep(ctx context.Context, sealedSecret []byte, step int64) (bool, error)
	CreateSession(ctx context.Context, session store.SessionRecord, provenAgainst store.Account) error
	Session(ctx context.Context, id string) (store.SessionRecord, bool, error)
	TouchSession(ctx context.Context, id string, seenAt time.Time) error
	Sessions(ctx context.Context) ([]store.SessionRecord, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteAllSessions(ctx context.Context) error
	PurgeExpiredSessions(ctx context.Context, now time.Time) (int64, error)
}

// AccountService regroupe le compte opérateur et ses sessions ; chaque appel relit le store, sans cache.
type AccountService interface {
	// AuthStatus dit si un compte existe et si WebAuthn est utilisable.
	AuthStatus(ctx context.Context) (AuthStatus, error)
	// SetupAccount crée le compte et ouvre une session ; le jeton d'amorçage est contrôlé par l'appelant.
	SetupAccount(ctx context.Context, username, password string, client ClientInfo) (SessionGrant, error)
	// Login vérifie le mot de passe ; il ouvre la session, ou rend les seconds facteurs à présenter.
	Login(ctx context.Context, username, password string, client ClientInfo) (LoginResult, error)
	// ResumeSession valide une session et date son dernier passage.
	ResumeSession(ctx context.Context, token string) (SessionView, error)
	// Logout ferme la session du jeton.
	Logout(ctx context.Context, token string) error
	// LogoutAll ferme toutes les sessions.
	LogoutAll(ctx context.Context) error
	// ChangePassword remplace le mot de passe après ressaisie de l'actuel et ferme toutes les autres sessions.
	ChangePassword(ctx context.Context, token, currentPassword, newPassword string) error
	// ListSessions liste les sessions ouvertes, celle du jeton marquée Current.
	ListSessions(ctx context.Context, token string) ([]SessionView, error)
	// ResetAccount efface le compte, ses clés, son TOTP et ses sessions.
	ResetAccount(ctx context.Context) error
	// AccountSummary résume le compte : identifiant, nombre de clés, TOTP.
	AccountSummary(ctx context.Context) (AccountSummary, error)
	// BeginWebAuthnRegistration ouvre l'enregistrement d'une clé (second_factor ou passkey), session ouverte et mot
	// de passe ressaisi.
	BeginWebAuthnRegistration(ctx context.Context, token, password, kind string) (WebAuthnCeremony, error)
	// FinishWebAuthnRegistration vérifie l'attestation et enregistre la clé sous son nom, si la session vaut encore.
	FinishWebAuthnRegistration(ctx context.Context, token string, state []byte, name string, body io.Reader) error
	// BeginWebAuthnLogin ouvre l'étape second facteur, limitée aux clés second_factor, sur le ticket rendu par Login
	// (vérifié sans être consommé).
	BeginWebAuthnLogin(ctx context.Context, ticket string) (WebAuthnCeremony, error)
	// FinishWebAuthnLogin consomme le ticket, vérifie l'assertion d'une clé second_factor sous le limiteur et ouvre la
	// session.
	FinishWebAuthnLogin(ctx context.Context, ticket string, state []byte, body io.Reader, client ClientInfo) (SessionGrant, error)
	// BeginPasskeyLogin ouvre une connexion découvrable par clé d'accès, vérification de l'utilisateur exigée.
	BeginPasskeyLogin(ctx context.Context) (WebAuthnCeremony, error)
	// FinishPasskeyLogin vérifie l'assertion d'une clé passkey sous le limiteur et ouvre la session sans mot de passe.
	FinishPasskeyLogin(ctx context.Context, state []byte, body io.Reader, client ClientInfo) (SessionGrant, error)
	// ListCredentials liste les clés, sans clé publique ni enregistrement.
	ListCredentials(ctx context.Context) ([]CredentialView, error)
	// RenameCredential renomme une clé ; l'appelant garantit la session.
	RenameCredential(ctx context.Context, id, name string) error
	// DeleteCredential supprime une clé, session ouverte et mot de passe ressaisi.
	DeleteCredential(ctx context.Context, token, id, password string) error
	// BeginTOTPSetup tire un secret TOTP en attente, session ouverte et mot de passe ressaisi ; l'appelant garde State
	// côté serveur.
	BeginTOTPSetup(ctx context.Context, token, password string) (TOTPEnrolment, error)
	// FinishTOTPSetup enregistre, scellé, le secret en attente une fois qu'un code valable le confirme.
	FinishTOTPSetup(ctx context.Context, token string, state []byte, code string) error
	// LoginWithTOTP consomme le ticket rendu par Login, vérifie le code sous le limiteur et ouvre la session.
	LoginWithTOTP(ctx context.Context, ticket, code string, client ClientInfo) (SessionGrant, error)
	// RemoveTOTP retire le TOTP, session ouverte et mot de passe ressaisi.
	RemoveTOTP(ctx context.Context, token, password string) error
}

// AuthStatus est l'état d'authentification public ; il ne porte jamais l'identifiant.
type AuthStatus struct {
	AccountExists     bool `json:"account_exists"`
	WebAuthnAvailable bool `json:"webauthn_available"`
}

// SessionGrant porte le jeton d'une session ouverte ; seul son condensé est stocké.
type SessionGrant struct {
	Token     string
	ExpiresAt time.Time
}

// ClientInfo décrit le client à titre d'information (liste des sessions).
type ClientInfo struct {
	Address, UserAgent string
}

// LoginResult porte la session émise, ou les seconds facteurs attendus avec le ticket (5 min, usage unique) que l'étape
// du second facteur exige.
type LoginResult struct {
	Session       *SessionGrant
	SecondFactors []string
	Ticket        string
}

// SessionView décrit une session ouverte.
type SessionView struct {
	Username              string
	Current               bool
	CreatedAt, LastSeenAt time.Time
	Address               string
}

// AccountSummary résume le compte pour la CLI.
type AccountSummary struct {
	Exists                     bool
	Username                   string
	SecondFactorKeys, Passkeys int
	TOTP                       bool
}

// throttledAttempt termine un essai admis par exactement un appel au limiteur ; sans verdict, il est annulé.
type throttledAttempt struct {
	throttle *auth.Throttle
	finish   func(key string)
}

func (attempt *throttledAttempt) fail()    { attempt.finish = attempt.throttle.RecordFailure }
func (attempt *throttledAttempt) succeed() { attempt.finish = attempt.throttle.RecordSuccess }
func (attempt *throttledAttempt) end()     { attempt.finish(loginThrottleKey) }

func (svc *service) beginAttempt() (*throttledAttempt, error) {
	throttle := svc.deps.Auth.Throttle
	if retryAfter, allowed := throttle.Allow(loginThrottleKey); !allowed {
		return nil, &ThrottledError{RetryAfter: retryAfter}
	}
	return &throttledAttempt{throttle: throttle, finish: throttle.Cancel}, nil
}

func (svc *service) runArgon(ctx context.Context, computation func()) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case argonSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-argonSlots }()
	svc.observeArgon(1)
	defer svc.observeArgon(-1)
	computation()
	return nil
}

func (svc *service) observeArgon(delta int32) {
	if svc.argonObserver != nil {
		svc.argonObserver(delta)
	}
}

func (svc *service) hashPassword(ctx context.Context, password string) (string, error) {
	var hash string
	var hashErr error
	if err := svc.runArgon(ctx, func() { hash, hashErr = auth.HashPassword(password) }); err != nil {
		return "", err
	}
	return hash, hashErr
}

func (svc *service) AuthStatus(ctx context.Context) (AuthStatus, error) {
	_, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return AuthStatus{}, err
	}
	_, _, webAuthnAvailable := svc.deps.Config.WebAuthnOrigin()
	return AuthStatus{AccountExists: found, WebAuthnAvailable: webAuthnAvailable}, nil
}

// normalizeLabel rogne un identifiant ou un nom de clé : 1 à 64 caractères, UTF-8 valide, sans caractère de contrôle.
func normalizeLabel(label string) (string, bool) {
	trimmed := strings.TrimSpace(label)
	runeCount := utf8.RuneCountInString(trimmed)
	validLength := runeCount >= 1 && runeCount <= maxLabelRunes
	if !validLength || !utf8.ValidString(trimmed) || strings.ContainsFunc(trimmed, unicode.IsControl) {
		return "", false
	}
	return trimmed, true
}

func normalizeUsername(username string) (string, error) {
	normalized, ok := normalizeLabel(username)
	if !ok {
		return "", ErrInvalidUsername
	}
	return normalized, nil
}

func (svc *service) SetupAccount(ctx context.Context, username, password string, client ClientInfo) (SessionGrant, error) {
	normalized, err := normalizeUsername(username)
	if err != nil {
		return SessionGrant{}, err
	}
	if err := auth.ValidatePassword(password); err != nil {
		return SessionGrant{}, err
	}
	_, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return SessionGrant{}, err
	}
	if found {
		return SessionGrant{}, ErrAccountExists
	}
	hash, err := svc.hashPassword(ctx, password)
	if err != nil {
		return SessionGrant{}, err
	}
	account := store.Account{Username: normalized, PasswordHash: hash, CreatedAt: svc.now()}
	if err := svc.deps.Store.CreateAccount(ctx, account); err != nil {
		return SessionGrant{}, err
	}
	return svc.issueSession(ctx, account, client)
}

func (svc *service) Login(ctx context.Context, username, password string, client ClientInfo) (LoginResult, error) {
	if len(password) > auth.MaxPasswordBytes {
		return LoginResult{}, ErrInvalidCredentials
	}
	attempt, err := svc.beginAttempt()
	if err != nil {
		return LoginResult{}, err
	}
	defer attempt.end()
	_, _ = svc.deps.Store.PurgeExpiredSessions(ctx, svc.now())

	typedUsername := strings.TrimSpace(username)
	sameUsername := func(account store.Account) bool {
		return subtle.ConstantTimeCompare([]byte(account.Username), []byte(typedUsername)) == 1
	}
	account, err := svc.matchPassword(ctx, attempt, password, sameUsername)
	if err != nil {
		return LoginResult{}, err
	}
	factors, err := svc.secondFactors(ctx)
	if err != nil {
		return LoginResult{}, err
	}
	if len(factors) > 0 {
		ticket, err := svc.issueLoginTicket(ctx)
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{SecondFactors: factors, Ticket: ticket}, nil
	}
	grant, err := svc.issueSession(ctx, account, client)
	if errors.Is(err, store.ErrAccountChanged) {
		attempt.fail()
		return LoginResult{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginResult{}, err
	}
	attempt.succeed()
	return LoginResult{Session: &grant}, nil
}

// verifyPasswordThrottled vérifie une ressaisie du mot de passe sous le limiteur ; un succès ne remet pas les échecs
// à zéro (seule une session émise le fait).
func (svc *service) verifyPasswordThrottled(ctx context.Context, password string) error {
	return svc.withVerifiedPassword(ctx, password, func(*throttledAttempt, store.Account) error { return nil })
}

// withVerifiedPassword vérifie une ressaisie du mot de passe sous le limiteur, puis lance then dans le même essai
// avec le compte vérifié.
func (svc *service) withVerifiedPassword(ctx context.Context, password string, then func(attempt *throttledAttempt, verified store.Account) error) error {
	if len(password) > auth.MaxPasswordBytes {
		return ErrInvalidCredentials
	}
	attempt, err := svc.beginAttempt()
	if err != nil {
		return err
	}
	defer attempt.end()
	anyUsername := func(store.Account) bool { return true }
	verified, err := svc.matchPassword(ctx, attempt, password, anyUsername)
	if err != nil {
		return err
	}
	return then(attempt, verified)
}

// matchPassword fait exactement un calcul argon2, que le compte existe ou non, et compte l'échec dans attempt ; il rend
// le compte tel qu'il a été vérifié, avec l'empreinte recalculée si elle a été remplacée.
func (svc *service) matchPassword(ctx context.Context, attempt *throttledAttempt, password string, usernameMatches func(store.Account) bool) (store.Account, error) {
	account, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return store.Account{}, err
	}
	if !found || !usernameMatches(account) {
		if err := svc.runArgon(ctx, func() { svc.deps.Auth.VerifyDummy(password) }); err != nil {
			return store.Account{}, err
		}
		attempt.fail()
		return store.Account{}, ErrInvalidCredentials
	}
	var matches, needsRehash bool
	var verifyErr error
	if err := svc.runArgon(ctx, func() { matches, needsRehash, verifyErr = auth.VerifyPassword(password, account.PasswordHash) }); err != nil {
		return store.Account{}, err
	}
	if verifyErr != nil {
		return store.Account{}, fmt.Errorf("verify password: %w", verifyErr)
	}
	if !matches {
		attempt.fail()
		return store.Account{}, ErrInvalidCredentials
	}
	if needsRehash {
		account.PasswordHash = svc.rehashBestEffort(ctx, account.PasswordHash, password)
	}
	return account, nil
}

// rehashBestEffort remplace l'empreinte seulement si elle n'a pas changé depuis la vérification et rend celle qui est
// désormais en place pour cette vérification.
func (svc *service) rehashBestEffort(ctx context.Context, verifiedHash, password string) string {
	hash, err := svc.hashPassword(ctx, password)
	if err != nil {
		return verifiedHash
	}
	replaced, err := svc.deps.Store.ReplacePasswordHash(ctx, verifiedHash, hash)
	if err != nil || !replaced {
		return verifiedHash
	}
	return hash
}

func (svc *service) secondFactors(ctx context.Context) ([]string, error) {
	credentials, err := svc.deps.Store.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	_, hasTOTP, err := svc.deps.Store.TOTP(ctx)
	if err != nil {
		return nil, err
	}
	var factors []string
	if countCredentials(credentials, credentialKindSecondFactor) > 0 {
		factors = append(factors, SecondFactorWebAuthn)
	}
	if hasTOTP {
		factors = append(factors, SecondFactorTOTP)
	}
	return factors, nil
}

func countCredentials(credentials []store.WebAuthnCredential, kind string) int {
	count := 0
	for _, credential := range credentials {
		if credential.Kind == kind {
			count++
		}
	}
	return count
}

// issueSession enregistre la session pour provenAgainst, le compte contre lequel la preuve a été vérifiée ;
// store.ErrAccountChanged si le compte a été remis à zéro, recréé ou a changé de mot de passe entre-temps.
func (svc *service) issueSession(ctx context.Context, provenAgainst store.Account, client ClientInfo) (SessionGrant, error) {
	token, id, err := auth.NewSessionToken()
	if err != nil {
		return SessionGrant{}, err
	}
	createdAt := svc.now()
	record := store.SessionRecord{
		ID: id, CreatedAt: createdAt, LastSeenAt: createdAt, ExpiresAt: createdAt.Add(auth.SessionAbsoluteLifetime),
		Address:   truncateUTF8(client.Address, maxClientAddressBytes),
		UserAgent: truncateUTF8(client.UserAgent, maxClientUserAgentBytes),
	}
	if err := svc.deps.Store.CreateSession(ctx, record, provenAgainst); err != nil {
		return SessionGrant{}, err
	}
	return SessionGrant{Token: token, ExpiresAt: record.ExpiresAt}, nil
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	return strings.ToValidUTF8(value[:maxBytes], "")
}

func sessionExpired(session store.SessionRecord, now time.Time) bool {
	return !now.Before(session.ExpiresAt) || now.Sub(session.LastSeenAt) >= auth.SessionIdleTimeout
}

// issuedForAccount est faux pour une session émise pour une génération antérieure du compte, ou d'avant la colonne de
// génération.
func issuedForAccount(session store.SessionRecord, account store.Account) bool {
	return session.AccountCreatedAt.Equal(account.CreatedAt)
}

// openSession valide le jeton et date son passage ; une session expirée, sans compte ou d'un autre compte est
// supprimée.
func (svc *service) openSession(ctx context.Context, token string) (store.SessionRecord, store.Account, error) {
	if token == "" {
		return store.SessionRecord{}, store.Account{}, ErrSessionInvalid
	}
	id := auth.SessionIDOf(token)
	session, found, err := svc.deps.Store.Session(ctx, id)
	if err != nil {
		return store.SessionRecord{}, store.Account{}, err
	}
	if !found {
		return store.SessionRecord{}, store.Account{}, ErrSessionInvalid
	}
	account, accountFound, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return store.SessionRecord{}, store.Account{}, err
	}
	now := svc.now()
	if !accountFound || !issuedForAccount(session, account) || sessionExpired(session, now) {
		_ = svc.deps.Store.DeleteSession(ctx, id)
		return store.SessionRecord{}, store.Account{}, ErrSessionInvalid
	}
	if err := svc.deps.Store.TouchSession(ctx, id, now); err != nil {
		return store.SessionRecord{}, store.Account{}, err
	}
	session.LastSeenAt = now
	return session, account, nil
}

func sessionView(session store.SessionRecord, username string, current bool) SessionView {
	return SessionView{
		Username: username, Current: current, CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, Address: session.Address,
	}
}

func (svc *service) ResumeSession(ctx context.Context, token string) (SessionView, error) {
	session, account, err := svc.openSession(ctx, token)
	if err != nil {
		return SessionView{}, err
	}
	return sessionView(session, account.Username, true), nil
}

func (svc *service) Logout(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return svc.deps.Store.DeleteSession(ctx, auth.SessionIDOf(token))
}

func (svc *service) LogoutAll(ctx context.Context) error {
	return svc.deps.Store.DeleteAllSessions(ctx)
}

func (svc *service) ChangePassword(ctx context.Context, token, currentPassword, newPassword string) error {
	current, _, err := svc.openSession(ctx, token)
	if err != nil {
		return err
	}
	if err := auth.ValidatePassword(newPassword); err != nil {
		return err
	}
	return svc.withVerifiedPassword(ctx, currentPassword, func(attempt *throttledAttempt, verified store.Account) error {
		return svc.commitPasswordChange(ctx, attempt, verified, newPassword, current.ID)
	})
}

// commitPasswordChange écrit le nouveau mot de passe seulement si le compte est encore celui vérifié ; sinon la
// ressaisie compte comme un échec.
func (svc *service) commitPasswordChange(ctx context.Context, attempt *throttledAttempt, verified store.Account, newPassword, keepSessionID string) error {
	hash, err := svc.hashPassword(ctx, newPassword)
	if err != nil {
		return err
	}
	err = svc.deps.Store.ChangePasswordHash(ctx, verified, hash, keepSessionID)
	switch {
	case errors.Is(err, store.ErrAccountChanged):
		attempt.fail()
		return ErrInvalidCredentials
	case errors.Is(err, store.ErrSessionNotFound):
		return ErrSessionInvalid
	default:
		return err
	}
}

func (svc *service) ListSessions(ctx context.Context, token string) ([]SessionView, error) {
	current, account, err := svc.openSession(ctx, token)
	if err != nil {
		return nil, err
	}
	sessions, err := svc.deps.Store.Sessions(ctx)
	if err != nil {
		return nil, err
	}
	now := svc.now()
	views := make([]SessionView, 0, len(sessions))
	for _, session := range sessions {
		if session.ID == current.ID {
			views = append(views, sessionView(current, account.Username, true))
			continue
		}
		if issuedForAccount(session, account) && !sessionExpired(session, now) {
			views = append(views, sessionView(session, account.Username, false))
		}
	}
	return views, nil
}

func (svc *service) ResetAccount(ctx context.Context) error {
	return svc.deps.Store.ResetAccount(ctx)
}

func (svc *service) AccountSummary(ctx context.Context) (AccountSummary, error) {
	account, found, err := svc.deps.Store.Account(ctx)
	if err != nil || !found {
		return AccountSummary{}, err
	}
	credentials, err := svc.deps.Store.Credentials(ctx)
	if err != nil {
		return AccountSummary{}, err
	}
	_, hasTOTP, err := svc.deps.Store.TOTP(ctx)
	if err != nil {
		return AccountSummary{}, err
	}
	return AccountSummary{
		Exists: true, Username: account.Username, TOTP: hasTOTP,
		SecondFactorKeys: countCredentials(credentials, credentialKindSecondFactor),
		Passkeys:         countCredentials(credentials, credentialKindPasskey),
	}, nil
}
