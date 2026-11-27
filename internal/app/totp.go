package app

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const (
	totpIssuer        = "Necromanciarr"
	totpPeriod        = 30 * time.Second
	totpSkewSteps     = 1
	totpSecretBytes   = 20
	totpCodeDigits    = 6
	totpQRCodePixels  = 256
	totpSetupLifetime = 10 * time.Minute
	totpSealKey       = "account.totp"
	totpPendingKey    = "account.totp.pending"
	totpCodeSeparator = " -"
	purposeTOTPSetup  = "totp_setup"
)

var totpCodeOptions = hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

var (
	// ErrTOTPUnavailable : sans NECROMANCIARR_SECRET_KEY, le secret TOTP ne peut pas être scellé.
	ErrTOTPUnavailable = errors.New("app: totp needs NECROMANCIARR_SECRET_KEY to seal its secret")
	// ErrInvalidCode : code TOTP faux, mal formé, déjà utilisé ou sans TOTP enregistré, sans distinction.
	ErrInvalidCode = errors.New("app: invalid or already used code")
)

var errTOTPQRCode = errors.New("render totp qr code")

// TOTPEnrolment est montré une fois à l'utilisateur authentifié ; State porte le secret en attente, scellé, que
// l'appelant garde côté serveur jusqu'à FinishTOTPSetup.
type TOTPEnrolment struct {
	OtpauthURI   string
	SecretBase32 string
	QRPNG        []byte
	State        []byte
}

func totpStepAt(moment time.Time) int64 {
	return moment.Unix() / int64(totpPeriod/time.Second)
}

// normalizeTOTPCode retire les espaces autour et au plus un espace ou tiret intérieur ; il faut ensuite 6 chiffres ASCII.
func normalizeTOTPCode(code string) (string, bool) {
	trimmed := strings.TrimSpace(code)
	separatorIndex := strings.IndexAny(trimmed, totpCodeSeparator)
	if insideCode := separatorIndex > 0 && separatorIndex < len(trimmed)-1; insideCode {
		trimmed = trimmed[:separatorIndex] + trimmed[separatorIndex+1:]
	}
	if len(trimmed) != totpCodeDigits {
		return "", false
	}
	for _, character := range []byte(trimmed) {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	return trimmed, true
}

// highestMatchingStep compare le code aux pas t-1, t et t+1 en temps constant et rend le plus haut qui correspond.
func highestMatchingStep(secret, code string, now time.Time) (int64, bool) {
	currentStep := totpStepAt(now)
	matchedStep, matched := int64(0), false
	for offset := int64(-totpSkewSteps); offset <= totpSkewSteps; offset++ {
		candidateStep := currentStep + offset
		expected, err := hotp.GenerateCodeCustom(secret, uint64(candidateStep), totpCodeOptions)
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			matchedStep, matched = candidateStep, true
		}
	}
	return matchedStep, matched
}

func (svc *service) totpSealer() (*config.FieldSealer, error) {
	sealer, err := svc.sealer()
	if err != nil {
		return nil, err
	}
	if sealer == nil {
		return nil, ErrTOTPUnavailable
	}
	return sealer, nil
}

func (svc *service) BeginTOTPSetup(ctx context.Context, token, password string) (TOTPEnrolment, error) {
	sealer, err := svc.totpSealer()
	if err != nil {
		return TOTPEnrolment{}, err
	}
	session, account, err := svc.openSession(ctx, token)
	if err != nil {
		return TOTPEnrolment{}, err
	}
	if err := svc.verifyPasswordThrottled(ctx, password); err != nil {
		return TOTPEnrolment{}, err
	}
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer: totpIssuer, AccountName: account.Username, Period: uint(totpPeriod / time.Second),
		SecretSize: totpSecretBytes, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
	})
	if err != nil {
		return TOTPEnrolment{}, fmt.Errorf("generate totp secret: %w", err)
	}
	qrCode, err := totpQRCode(key)
	if err != nil {
		return TOTPEnrolment{}, err
	}
	sealedPending, err := sealer.SealSetting(totpPendingKey, key.Secret())
	if err != nil {
		return TOTPEnrolment{}, err
	}
	state, err := svc.sealEnvelope(ceremonyEnvelope{
		Purpose: purposeTOTPSetup, TicketHash: session.ID, TOTPSecret: sealedPending,
		Session: webauthn.SessionData{Expires: svc.now().Add(totpSetupLifetime)},
	})
	if err != nil {
		return TOTPEnrolment{}, err
	}
	return TOTPEnrolment{OtpauthURI: key.URL(), SecretBase32: key.Secret(), QRPNG: qrCode, State: state}, nil
}

// totpQRCode rend l'URI otpauth en PNG ; l'erreur de la bibliothèque n'est pas reprise, elle pourrait citer l'URI.
func totpQRCode(key *otp.Key) ([]byte, error) {
	image, err := key.Image(totpQRCodePixels, totpQRCodePixels)
	if err != nil {
		return nil, errTOTPQRCode
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image); err != nil {
		return nil, errTOTPQRCode
	}
	return encoded.Bytes(), nil
}

// FinishTOTPSetup lit le code avant l'état, pour qu'une faute de frappe ne brûle pas l'état ; le pas confirmé devient
// le dernier pas accepté, donc le code d'enregistrement ne se rejoue pas.
func (svc *service) FinishTOTPSetup(ctx context.Context, token string, state []byte, code string) error {
	sealer, err := svc.totpSealer()
	if err != nil {
		return err
	}
	session, _, err := svc.openSession(ctx, token)
	if err != nil {
		return err
	}
	normalizedCode, valid := normalizeTOTPCode(code)
	if !valid {
		return ErrInvalidCode
	}
	pendingSecret, err := svc.openTOTPSetup(sealer, state, session.ID)
	if err != nil {
		return err
	}
	confirmedStep, matched := highestMatchingStep(pendingSecret, normalizedCode, svc.now())
	if !matched {
		return ErrInvalidCode
	}
	sealedSecret, err := sealer.SealSetting(totpSealKey, pendingSecret)
	if err != nil {
		return err
	}
	return svc.deps.Store.SetTOTP(ctx, store.TOTPFactor{SecretSealed: []byte(sealedSecret), LastStep: confirmedStep, CreatedAt: svc.now()})
}

// openTOTPSetup ouvre un état totp_setup de cette session, brûle son nonce et descelle le secret en attente ; tout
// refus vaut ErrInvalidCode.
func (svc *service) openTOTPSetup(sealer *config.FieldSealer, state []byte, sessionID string) (string, error) {
	envelope, err := svc.readEnvelope(state, ceremonyBinding{purpose: purposeTOTPSetup, ticketHash: sessionID}, totpSetupLifetime)
	if err != nil {
		return "", asInvalidCode(err)
	}
	if err := svc.burnNonce(envelope); err != nil {
		return "", asInvalidCode(err)
	}
	pendingSecret, err := sealer.OpenSetting(totpPendingKey, envelope.TOTPSecret)
	if err != nil {
		return "", ErrInvalidCode
	}
	return pendingSecret, nil
}

func asInvalidCode(err error) error {
	if errors.Is(err, ErrInvalidAssertion) {
		return ErrInvalidCode
	}
	return err
}

// LoginWithTOTP suit l'ordre de FinishWebAuthnLogin : le ticket est brûlé sous le limiteur avant de lire le code ; un
// essai refusé par le limiteur ne l'atteint pas et le garde.
func (svc *service) LoginWithTOTP(ctx context.Context, ticket, code string, client ClientInfo) (SessionGrant, error) {
	sealer, err := svc.totpSealer()
	if err != nil {
		return SessionGrant{}, err
	}
	attempt, err := svc.beginAttempt()
	if err != nil {
		return SessionGrant{}, err
	}
	defer attempt.end()
	if _, err := svc.consumeLoginTicket(ctx, ticket); err != nil {
		if errors.Is(err, ErrLoginTicketInvalid) {
			attempt.fail()
		}
		return SessionGrant{}, err
	}
	if err := svc.acceptTOTPCode(ctx, sealer, code); err != nil {
		if errors.Is(err, ErrInvalidCode) {
			attempt.fail()
		}
		return SessionGrant{}, err
	}
	grant, err := svc.issueSession(ctx, client)
	if err != nil {
		return SessionGrant{}, err
	}
	attempt.succeed()
	return grant, nil
}

// acceptTOTPCode relit le facteur, descelle le secret et accepte chaque pas une seule fois, même sous appels
// concurrents ; tout refus vaut ErrInvalidCode, seule une erreur du store passe telle quelle.
func (svc *service) acceptTOTPCode(ctx context.Context, sealer *config.FieldSealer, code string) error {
	normalizedCode, valid := normalizeTOTPCode(code)
	if !valid {
		return ErrInvalidCode
	}
	factor, found, err := svc.deps.Store.TOTP(ctx)
	if err != nil {
		return err
	}
	if !found {
		return ErrInvalidCode
	}
	secret, err := sealer.OpenSetting(totpSealKey, string(factor.SecretSealed))
	if err != nil {
		return ErrInvalidCode
	}
	matchedStep, matched := highestMatchingStep(secret, normalizedCode, svc.now())
	if !matched {
		return ErrInvalidCode
	}
	advanced, err := svc.deps.Store.AdvanceTOTPStep(ctx, matchedStep)
	if err != nil {
		return err
	}
	if !advanced {
		return ErrInvalidCode
	}
	return nil
}

func (svc *service) RemoveTOTP(ctx context.Context, token, password string) error {
	if _, _, err := svc.openSession(ctx, token); err != nil {
		return err
	}
	if err := svc.verifyPasswordThrottled(ctx, password); err != nil {
		return err
	}
	return svc.deps.Store.DeleteTOTP(ctx)
}
