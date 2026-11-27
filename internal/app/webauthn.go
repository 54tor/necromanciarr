package app

import (
	"bytes"
	"context"
	"crypto/rand"
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

	"necromanciarr/internal/auth"
	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const (
	webAuthnDisplayName      = "Necromanciarr"
	webAuthnCeremonyTimeout  = 5 * time.Minute
	webAuthnUserHandlePrefix = "necromanciarr-account:"
	ceremonyEnvelopeVersion  = 1
	ceremonyNonceBytes       = 16
	purposeRegistration      = "registration"
	purposeLoginSecondFactor = "login_second_factor"
	purposeLoginPasskey      = "login_passkey"
)

var registrableKinds = []string{credentialKindSecondFactor, credentialKindPasskey}

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

// ceremonyEnvelope est l'état rendu à l'appelant : l'usage de la cérémonie, la sorte de clé enregistrée, un nonce à
// usage unique, l'empreinte du ticket du second facteur et la SessionData de la bibliothèque.
type ceremonyEnvelope struct {
	Version    int                  `json:"v"`
	Purpose    string               `json:"purpose"`
	Kind       string               `json:"kind,omitempty"`
	Nonce      []byte               `json:"nonce"`
	TicketHash string               `json:"ticketHash,omitempty"`
	Session    webauthn.SessionData `json:"session"`
	TOTPSecret string               `json:"totpSecret,omitempty"`
}

// ceremonyBinding dit à quoi sert un état et à quel ticket il est lié.
type ceremonyBinding struct {
	purpose, kind, ticketHash string
}

// newCeremony date l'expiration sur l'horloge du service, que finish contrôle en plus de la bibliothèque.
func (svc *service) newCeremony(options any, session *webauthn.SessionData, binding ceremonyBinding) (WebAuthnCeremony, error) {
	session.Expires = svc.now().Add(webAuthnCeremonyTimeout)
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return WebAuthnCeremony{}, fmt.Errorf("encode webauthn options: %w", err)
	}
	state, err := svc.sealEnvelope(ceremonyEnvelope{
		Purpose: binding.purpose, Kind: binding.kind, TicketHash: binding.ticketHash, Session: *session,
	})
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	return WebAuthnCeremony{Options: optionsJSON, State: state}, nil
}

// sealEnvelope pose la version et un nonce neuf, puis scelle l'état.
func (svc *service) sealEnvelope(envelope ceremonyEnvelope) ([]byte, error) {
	nonce := make([]byte, ceremonyNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate ceremony nonce: %w", err)
	}
	envelope.Version, envelope.Nonce = ceremonyEnvelopeVersion, nonce
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, fmt.Errorf("encode ceremony state: %w", err)
	}
	return svc.sealState(payload)
}

// openCeremony vérifie d'abord le sceau, puis refuse un état illisible, d'un autre usage ou d'un autre ticket, expiré, lié à un autre RP ID ou une
// autre origine, puis brûle son nonce : un état ne sert qu'une fois, que la preuve qui suit réussisse ou non.
func (svc *service) openCeremony(state []byte, binding ceremonyBinding, instance *webauthn.WebAuthn) (ceremonyEnvelope, error) {
	envelope, err := svc.readEnvelope(state, binding, webAuthnCeremonyTimeout)
	if err != nil {
		return ceremonyEnvelope{}, err
	}
	sameRelyingParty := envelope.Session.RelyingPartyID == instance.Config.RPID && envelope.Session.Origin == ""
	knownKind := binding.purpose != purposeRegistration || slices.Contains(registrableKinds, envelope.Kind)
	if !sameRelyingParty || !knownKind {
		return ceremonyEnvelope{}, ErrInvalidAssertion
	}
	if err := svc.burnNonce(envelope); err != nil {
		return ceremonyEnvelope{}, err
	}
	return envelope, nil
}

// readEnvelope vérifie le sceau avant de décoder, puis la version, le nonce, l'usage, le ticket et une expiration
// comprise dans lifetime ; il ne brûle rien.
func (svc *service) readEnvelope(state []byte, binding ceremonyBinding, lifetime time.Duration) (ceremonyEnvelope, error) {
	payload, err := svc.unsealState(state)
	if err != nil {
		return ceremonyEnvelope{}, err
	}
	var envelope ceremonyEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ceremonyEnvelope{}, ErrInvalidAssertion
	}
	now := svc.now()
	expires := envelope.Session.Expires
	wellFormed := envelope.Version == ceremonyEnvelopeVersion && len(envelope.Nonce) == ceremonyNonceBytes &&
		envelope.Purpose == binding.purpose && envelope.TicketHash == binding.ticketHash
	live := !expires.IsZero() && now.Before(expires) && !expires.After(now.Add(lifetime))
	if !wellFormed || !live {
		return ceremonyEnvelope{}, ErrInvalidAssertion
	}
	return envelope, nil
}

// burnNonce marque le nonce de l'état comme présenté ; un ensemble plein refuse au lieu d'évincer, car un nonce évincé
// serait rejouable tant que son état vit.
func (svc *service) burnNonce(envelope ceremonyEnvelope) error {
	expires := envelope.Session.Expires
	if !svc.burnedNonces.insertIfAbsent(string(envelope.Nonce), struct{}{}, expires.Add(burnedNonceMargin), svc.now()) {
		return ErrInvalidAssertion
	}
	return nil
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
	return svc.newCeremony(creation, session, ceremonyBinding{purpose: purposeRegistration, kind: kind})
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
	_, account, err := svc.openSession(ctx, token)
	if err != nil {
		return err
	}
	envelope, err := svc.openCeremony(state, ceremonyBinding{purpose: purposeRegistration}, instance)
	if err != nil {
		return err
	}
	user, err := svc.webAuthnUser(ctx, account, "")
	if err != nil {
		return err
	}
	kind := envelope.Kind
	credential, err := createCredential(instance, user, envelope.Session, body, kind)
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

func (svc *service) BeginWebAuthnLogin(ctx context.Context, ticket string) (WebAuthnCeremony, error) {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return WebAuthnCeremony{}, err
	}
	account, err := svc.peekLoginTicket(ctx, ticket)
	if err != nil {
		return WebAuthnCeremony{}, err
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
	return svc.newCeremony(assertion, session, ceremonyBinding{purpose: purposeLoginSecondFactor, ticketHash: auth.SessionIDOf(ticket)})
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
	return svc.newCeremony(assertion, session, ceremonyBinding{purpose: purposeLoginPasskey})
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

// assertionStep décrit une connexion par clé : l'état attendu, la sorte de clé admise et sa vérification.
// assertionStep décrit une étape d'assertion ; ticketAccount, posé pour l'étape second facteur, est le compte que le
// ticket consommé a validé : l'assertion n'est acceptée que contre lui.
type assertionStep struct {
	binding       ceremonyBinding
	kind          string
	validate      assertionValidator
	ticketAccount *store.Account
}

// sameAccountSnapshot est vrai si le compte relu est encore la génération et l'empreinte de expected.
func sameAccountSnapshot(current store.Account, found bool, expected store.Account) bool {
	return found && current.CreatedAt.Equal(expected.CreatedAt) && current.PasswordHash == expected.PasswordHash
}

// FinishWebAuthnLogin brûle le ticket sous le limiteur avant toute vérification ; un essai refusé par le limiteur ne
// l'atteint pas et le garde.
func (svc *service) FinishWebAuthnLogin(ctx context.Context, ticket string, state []byte, body io.Reader, client ClientInfo) (SessionGrant, error) {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return SessionGrant{}, err
	}
	attempt, err := svc.beginAttempt()
	if err != nil {
		return SessionGrant{}, err
	}
	defer attempt.end()
	ticketAccount, err := svc.consumeLoginTicket(ctx, ticket)
	if err != nil {
		if errors.Is(err, ErrLoginTicketInvalid) {
			attempt.fail()
		}
		return SessionGrant{}, err
	}
	step := assertionStep{
		binding:       ceremonyBinding{purpose: purposeLoginSecondFactor, ticketHash: auth.SessionIDOf(ticket)},
		kind:          credentialKindSecondFactor,
		validate:      validateSecondFactorAssertion,
		ticketAccount: &ticketAccount,
	}
	return svc.finishAssertion(ctx, instance, attempt, step, state, body, client)
}

func (svc *service) FinishPasskeyLogin(ctx context.Context, state []byte, body io.Reader, client ClientInfo) (SessionGrant, error) {
	instance, err := svc.webAuthnInstance()
	if err != nil {
		return SessionGrant{}, err
	}
	attempt, err := svc.beginAttempt()
	if err != nil {
		return SessionGrant{}, err
	}
	defer attempt.end()
	step := assertionStep{
		binding: ceremonyBinding{purpose: purposeLoginPasskey}, kind: credentialKindPasskey, validate: validatePasskeyAssertion,
	}
	return svc.finishAssertion(ctx, instance, attempt, step, state, body, client)
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

// finishAssertion vérifie une assertion dans un essai admis : un état refusé ou rejoué, une preuve refusée ou un
// compteur en recul comptent un échec, une erreur interne annule l'essai, seule une session émise remet à zéro.
func (svc *service) finishAssertion(ctx context.Context, instance *webauthn.WebAuthn, attempt *throttledAttempt, step assertionStep, state []byte, body io.Reader, client ClientInfo) (SessionGrant, error) {
	envelope, err := svc.openCeremony(state, step.binding, instance)
	if err != nil {
		if errors.Is(err, ErrInvalidAssertion) {
			attempt.fail()
		}
		return SessionGrant{}, err
	}
	account, found, err := svc.deps.Store.Account(ctx)
	if err != nil {
		return SessionGrant{}, err
	}
	if step.ticketAccount != nil && !sameAccountSnapshot(account, found, *step.ticketAccount) {
		attempt.fail()
		return SessionGrant{}, ErrInvalidAssertion
	}
	user := &accountUser{}
	if found {
		if user, err = svc.webAuthnUser(ctx, account, step.kind); err != nil {
			return SessionGrant{}, err
		}
	}
	credential, verifyErr := verifyAssertion(instance, user, envelope.Session, body, step.validate)
	if !found || verifyErr != nil || credential.Authenticator.CloneWarning {
		attempt.fail()
		return SessionGrant{}, ErrInvalidAssertion
	}
	grant, err := svc.recordUseAndIssueSession(ctx, account, credential, client)
	if errors.Is(err, store.ErrAccountChanged) {
		attempt.fail()
		return SessionGrant{}, ErrInvalidAssertion
	}
	if err != nil {
		return SessionGrant{}, err
	}
	attempt.succeed()
	return grant, nil
}

func verifyAssertion(instance *webauthn.WebAuthn, user *accountUser, session webauthn.SessionData, body io.Reader, validate assertionValidator) (*webauthn.Credential, error) {
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

func (svc *service) recordUseAndIssueSession(ctx context.Context, provenAgainst store.Account, credential *webauthn.Credential, client ClientInfo) (SessionGrant, error) {
	record, err := json.Marshal(credential)
	if err != nil {
		return SessionGrant{}, fmt.Errorf("encode security key record: %w", err)
	}
	if err := svc.deps.Store.UpdateCredential(ctx, credential.ID, record, svc.now()); err != nil {
		return SessionGrant{}, err
	}
	return svc.issueSession(ctx, provenAgainst, client)
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
