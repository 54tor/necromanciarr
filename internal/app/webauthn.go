package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const (
	webAuthnDisplayName      = "Necromanciarr"
	webAuthnCeremonyTimeout  = 5 * time.Minute
	webAuthnUserHandlePrefix = "necromanciarr-account:"
)

var (
	// ErrWebAuthnUnavailable : sans publicUrl https (ou hors localhost), WebAuthn ne peut pas fonctionner.
	ErrWebAuthnUnavailable = errors.New("app: webauthn is unavailable without an https public url")
	// ErrCredentialNotFound : aucune clé de sécurité ne correspond.
	ErrCredentialNotFound = errors.New("app: security key not found")
	// ErrInvalidAssertion : la preuve WebAuthn (enregistrement ou connexion) n'est pas valable ; aucun détail exposé.
	ErrInvalidAssertion = errors.New("app: security key verification failed")
	// ErrInvalidCredentialName : nom de clé vide, trop long ou avec un caractère de contrôle.
	ErrInvalidCredentialName = errors.New("app: invalid security key name")
	// ErrInvalidCredentialKind : sorte de clé inconnue (second_factor ou passkey).
	ErrInvalidCredentialKind = errors.New("app: invalid security key kind")
)

// WebAuthnCeremony porte les options pour le navigateur ({"publicKey": …}) et l'état que l'appelant garde côté serveur
// pour l'étape finish.
type WebAuthnCeremony struct {
	Options json.RawMessage
	State   []byte
}

// CredentialView décrit une clé de sécurité ; ID est l'identifiant de credential en base64url, jamais la clé publique.
type CredentialView struct {
	ID         string
	Name, Kind string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// accountUser est l'unique utilisateur WebAuthn, tiré du compte opérateur.
type accountUser struct {
	handle      []byte
	username    string
	credentials []webauthn.Credential
}

func (user *accountUser) WebAuthnID() []byte                         { return user.handle }
func (user *accountUser) WebAuthnName() string                       { return user.username }
func (user *accountUser) WebAuthnDisplayName() string                { return user.username }
func (user *accountUser) WebAuthnCredentials() []webauthn.Credential { return user.credentials }

func accountUserHandle(account store.Account) []byte {
	seed := webAuthnUserHandlePrefix + account.Username + strconv.FormatInt(account.CreatedAt.UnixNano(), 10)
	digest := sha256.Sum256([]byte(seed))
	return digest[:]
}

func newWebAuthn(settings config.Config) (*webauthn.WebAuthn, error) {
	origin, rpID, available := settings.WebAuthnOrigin()
	if !available {
		return nil, ErrWebAuthnUnavailable
	}
	timeout := webauthn.TimeoutConfig{Enforce: true, Timeout: webAuthnCeremonyTimeout, TimeoutUVD: webAuthnCeremonyTimeout}
	instance, err := webauthn.New(&webauthn.Config{
		RPID: rpID, RPDisplayName: webAuthnDisplayName, RPOrigins: []string{origin},
		Timeouts: webauthn.TimeoutsConfig{Login: timeout, Registration: timeout},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: invalid relying party", ErrWebAuthnUnavailable)
	}
	return instance, nil
}

func (svc *service) webAuthnInstance() (*webauthn.WebAuthn, error) {
	svc.webAuthnOnce.Do(func() { svc.webAuthn, svc.webAuthnErr = newWebAuthn(svc.deps.Config) })
	return svc.webAuthn, svc.webAuthnErr
}

// webAuthnUser bâtit l'utilisateur avec les seules clés de la sorte donnée (toutes si kind est vide).
func (svc *service) webAuthnUser(ctx context.Context, account store.Account, kind string) (*accountUser, error) {
	stored, err := svc.deps.Store.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	user := &accountUser{handle: accountUserHandle(account), username: account.Username}
	for _, storedCredential := range stored {
		if kind != "" && storedCredential.Kind != kind {
			continue
		}
		var credential webauthn.Credential
		if err := json.Unmarshal(storedCredential.Record, &credential); err != nil {
			return nil, fmt.Errorf("decode security key record: %w", err)
		}
		user.credentials = append(user.credentials, credential)
	}
	return user, nil
}

func newCeremony(options any, session *webauthn.SessionData) (WebAuthnCeremony, error) {
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return WebAuthnCeremony{}, fmt.Errorf("encode webauthn options: %w", err)
	}
	state, err := json.Marshal(session)
	if err != nil {
		return WebAuthnCeremony{}, fmt.Errorf("encode webauthn state: %w", err)
	}
	return WebAuthnCeremony{Options: optionsJSON, State: state}, nil
}

// decodeCeremonyState refuse un état illisible ou lié à un autre RP ID ou une autre origine que la configuration.
func decodeCeremonyState(state []byte, instance *webauthn.WebAuthn) (webauthn.SessionData, error) {
	var session webauthn.SessionData
	if err := json.Unmarshal(state, &session); err != nil {
		return webauthn.SessionData{}, ErrInvalidAssertion
	}
	if session.RelyingPartyID != instance.Config.RPID || session.Origin != "" {
		return webauthn.SessionData{}, ErrInvalidAssertion
	}
	return session, nil
}

func registrationOptionsFor(kind string) ([]webauthn.RegistrationOption, error) {
	switch kind {
	case credentialKindSecondFactor:
		selection := protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementDiscouraged, RequireResidentKey: protocol.ResidentKeyNotRequired(),
			UserVerification: protocol.VerificationPreferred,
		}
		return []webauthn.RegistrationOption{webauthn.WithAuthenticatorSelection(selection)}, nil
	case credentialKindPasskey:
		selection := protocol.AuthenticatorSelection{
			ResidentKey: protocol.ResidentKeyRequirementRequired, RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification: protocol.VerificationRequired,
		}
		return []webauthn.RegistrationOption{
			webauthn.WithAuthenticatorSelection(selection), webauthn.WithExtensions(webauthn.WithExtensionCredProps()),
		}, nil
	default:
		return nil, ErrInvalidCredentialKind
	}
}

// registrationKindOf : seule une clé d'accès est enregistrée avec la vérification de l'utilisateur exigée.
func registrationKindOf(session webauthn.SessionData) string {
	if session.UserVerification == protocol.VerificationRequired {
		return credentialKindPasskey
	}
	return credentialKindSecondFactor
}

func normalizeCredentialName(name string) (string, error) {
	normalized, ok := normalizeLabel(name)
	if !ok {
		return "", ErrInvalidCredentialName
	}
	return normalized, nil
}

func (svc *service) BeginWebAuthnRegistration(ctx context.Context, token, password, kind string) (WebAuthnCeremony, error) {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	options, err := registrationOptionsFor(kind)
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	_, account, err := svc.openSession(ctx, token)
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	if err := svc.verifyPasswordThrottled(ctx, password); err != nil {
		return WebAuthnCeremony{}, err
	}
	user, err := svc.webAuthnUser(ctx, account, "")
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	exclusions := webauthn.WithExclusions(webauthn.Credentials(user.credentials).CredentialDescriptors())
	creation, session, err := instance.BeginRegistration(user, append(options, exclusions)...)
	if err != nil {
		return WebAuthnCeremony{}, fmt.Errorf("begin webauthn registration: %w", err)
	}
	return newCeremony(creation, session)
}

func (svc *service) FinishWebAuthnRegistration(ctx context.Context, token string, state []byte, name string, body io.Reader) error {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return err
	}
	normalizedName, err := normalizeCredentialName(name)
	if err != nil {
		return err
	}
	session, err := decodeCeremonyState(state, instance)
	if err != nil {
		return err
	}
	_, account, err := svc.openSession(ctx, token)
	if err != nil {
		return err
	}
	user, err := svc.webAuthnUser(ctx, account, "")
	if err != nil {
		return err
	}
	kind := registrationKindOf(session)
	credential, err := createCredential(instance, user, session, body, kind)
	if err != nil {
		return err
	}
	record, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode security key record: %w", err)
	}
	stored := store.WebAuthnCredential{ID: credential.ID, Name: normalizedName, Kind: kind, Record: record, CreatedAt: svc.now()}
	return svc.deps.Store.AddCredential(ctx, stored)
}

// createCredential vérifie l'attestation ; une clé d'accès doit avoir vérifié l'utilisateur et ne pas se dire non
// découvrable ; une clé déjà enregistrée est refusée.
func createCredential(instance *webauthn.WebAuthn, user *accountUser, session webauthn.SessionData, body io.Reader, kind string) (*webauthn.Credential, error) {
	parsed, err := protocol.ParseCredentialCreationResponseBody(body)
	if err != nil {
		return nil, ErrInvalidAssertion
	}
	credential, err := instance.CreateCredential(user, session, parsed)
	if err != nil {
		return nil, ErrInvalidAssertion
	}
	alreadyRegistered := slices.ContainsFunc(user.credentials, func(existing webauthn.Credential) bool {
		return bytes.Equal(existing.ID, credential.ID)
	})
	reportedNotResident := credential.Extensions.RK != nil && !*credential.Extensions.RK
	passkeyRequirementsMissed := kind == credentialKindPasskey && (!credential.Flags.UserVerified || reportedNotResident)
	if alreadyRegistered || passkeyRequirementsMissed {
		return nil, ErrInvalidAssertion
	}
	return credential, nil
}

func (svc *service) BeginWebAuthnLogin(ctx context.Context) (WebAuthnCeremony, error) {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	account, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	if !found {
		return WebAuthnCeremony{}, ErrCredentialNotFound
	}
	user, err := svc.webAuthnUser(ctx, account, credentialKindSecondFactor)
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	if len(user.credentials) == 0 {
		return WebAuthnCeremony{}, ErrCredentialNotFound
	}
	assertion, session, err := instance.BeginLogin(user, webauthn.WithUserVerification(protocol.VerificationPreferred))
	if err != nil {
		return WebAuthnCeremony{}, fmt.Errorf("begin webauthn login: %w", err)
	}
	return newCeremony(assertion, session)
}

func (svc *service) BeginPasskeyLogin(context.Context) (WebAuthnCeremony, error) {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	assertion, session, err := instance.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		return WebAuthnCeremony{}, fmt.Errorf("begin passkey login: %w", err)
	}
	return newCeremony(assertion, session)
}

// assertionValidator vérifie une assertion déjà lue pour l'utilisateur, limité aux clés de la bonne sorte.
type assertionValidator func(instance *webauthn.WebAuthn, user *accountUser, session webauthn.SessionData, parsed *protocol.ParsedCredentialAssertionData) (*webauthn.Credential, error)

func validateSecondFactorAssertion(instance *webauthn.WebAuthn, user *accountUser, session webauthn.SessionData, parsed *protocol.ParsedCredentialAssertionData) (*webauthn.Credential, error) {
	return instance.ValidateLogin(user, session, parsed)
}

// validatePasskeyAssertion exige le drapeau UV de l'assertion elle-même, quel que soit l'état transmis.
func validatePasskeyAssertion(instance *webauthn.WebAuthn, user *accountUser, session webauthn.SessionData, parsed *protocol.ParsedCredentialAssertionData) (*webauthn.Credential, error) {
	if !parsed.Response.AuthenticatorData.Flags.HasUserVerified() {
		return nil, ErrInvalidAssertion
	}
	_, credential, err := instance.ValidatePasskeyLogin(singleUserHandler(user), session, parsed)
	return credential, err
}

func (svc *service) FinishWebAuthnLogin(ctx context.Context, state []byte, body io.Reader, client ClientInfo) (SessionGrant, error) {
	return svc.finishAssertion(ctx, state, body, client, credentialKindSecondFactor, validateSecondFactorAssertion)
}

func (svc *service) FinishPasskeyLogin(ctx context.Context, state []byte, body io.Reader, client ClientInfo) (SessionGrant, error) {
	return svc.finishAssertion(ctx, state, body, client, credentialKindPasskey, validatePasskeyAssertion)
}

var errUnknownUserHandle = errors.New("unknown user handle")

func singleUserHandler(user *accountUser) webauthn.DiscoverableUserHandler {
	return func(_, userHandle []byte) (webauthn.User, error) {
		if !bytes.Equal(userHandle, user.handle) {
			return nil, errUnknownUserHandle
		}
		return user, nil
	}
}

// finishAssertion passe par le limiteur : une preuve refusée compte un échec, une erreur interne annule l'essai, seule
// une session émise remet les échecs à zéro.
func (svc *service) finishAssertion(ctx context.Context, state []byte, body io.Reader, client ClientInfo, kind string, validate assertionValidator) (SessionGrant, error) {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return SessionGrant{}, err
	}
	attempt, err := svc.beginAttempt()
	if err != nil {
		return SessionGrant{}, err
	}
	defer attempt.end()
	account, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return SessionGrant{}, err
	}
	user := &accountUser{}
	if found {
		if user, err = svc.webAuthnUser(ctx, account, kind); err != nil {
			return SessionGrant{}, err
		}
	}
	credential, verifyErr := verifyAssertion(instance, user, state, body, validate)
	if !found || verifyErr != nil || credential.Authenticator.CloneWarning {
		attempt.fail()
		return SessionGrant{}, ErrInvalidAssertion
	}
	grant, err := svc.recordUseAndIssueSession(ctx, credential, client)
	if err != nil {
		return SessionGrant{}, err
	}
	attempt.succeed()
	return grant, nil
}

func verifyAssertion(instance *webauthn.WebAuthn, user *accountUser, state []byte, body io.Reader, validate assertionValidator) (*webauthn.Credential, error) {
	session, err := decodeCeremonyState(state, instance)
	if err != nil {
		return nil, err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(body)
	if err != nil {
		return nil, ErrInvalidAssertion
	}
	credential, err := validate(instance, user, session, parsed)
	if err != nil {
		return nil, ErrInvalidAssertion
	}
	return credential, nil
}

func (svc *service) recordUseAndIssueSession(ctx context.Context, credential *webauthn.Credential, client ClientInfo) (SessionGrant, error) {
	record, err := json.Marshal(credential)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("encode security key record: %w", err)
	}
	if err := svc.deps.Store.UpdateCredential(ctx, credential.ID, record, svc.now()); err != nil {
		return SessionGrant{}, err
	}
	return svc.issueSession(ctx, client)
}

func (svc *service) ListCredentials(ctx context.Context) ([]CredentialView, error) {
	stored, err := svc.deps.Store.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]CredentialView, 0, len(stored))
	for _, credential := range stored {
		views = append(views, CredentialView{
			ID: base64.RawURLEncoding.EncodeToString(credential.ID), Name: credential.Name, Kind: credential.Kind,
			CreatedAt: credential.CreatedAt, LastUsedAt: credential.LastUsedAt,
		})
	}
	return views, nil
}

// findCredential décode l'identifiant base64url et vérifie que la clé existe.
func (svc *service) findCredential(ctx context.Context, encodedID string) ([]byte, error) {
	id, err := base64.RawURLEncoding.DecodeString(encodedID)
	if err != nil {
		return nil, ErrCredentialNotFound
	}
	stored, err := svc.deps.Store.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	exists := slices.ContainsFunc(stored, func(credential store.WebAuthnCredential) bool { return bytes.Equal(credential.ID, id) })
	if !exists {
		return nil, ErrCredentialNotFound
	}
	return id, nil
}

func (svc *service) RenameCredential(ctx context.Context, id, name string) error {
	normalizedName, err := normalizeCredentialName(name)
	if err != nil {
		return err
	}
	credentialID, err := svc.findCredential(ctx, id)
	if err != nil {
		return err
	}
	return svc.deps.Store.RenameCredential(ctx, credentialID, normalizedName)
}

func (svc *service) DeleteCredential(ctx context.Context, token, id, password string) error {
	if _, _, err := svc.openSession(ctx, token); err != nil {
		return err
	}
	credentialID, err := svc.findCredential(ctx, id)
	if err != nil {
		return err
	}
	if err := svc.verifyPasswordThrottled(ctx, password); err != nil {
		return err
	}
	return svc.deps.Store.DeleteCredential(ctx, credentialID)
}
