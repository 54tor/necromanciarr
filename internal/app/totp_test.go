package app

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"

	"necromanciarr/internal/config"
	"necromanciarr/internal/store"
)

const (
	stepsAfterEnrolment = 10
	concurrentAttempts  = 8
	wrongStepOffset     = 1000
)

var (
	testSecretKey    = bytes.Repeat([]byte{0x07}, config.SecretKeySize)
	pngSignature     = []byte("\x89PNG\r\n\x1a\n")
	testTOTPSettings = config.Config{SecretKey: testSecretKey}
)

type totpFixture struct {
	*accountFixture
	grant  SessionGrant
	secret string
}

func newTOTPFixture(t *testing.T) *totpFixture {
	t.Helper()
	fixture := newAccountFixture(t)
	fixture.service.deps.Config = testTOTPSettings
	return &totpFixture{accountFixture: fixture, grant: fixture.setup(t)}
}

// newEnrolledTOTPFixture enregistre le TOTP puis avance l'horloge, pour que les pas de la fenêtre soient neufs.
func newEnrolledTOTPFixture(t *testing.T) *totpFixture {
	t.Helper()
	fixture := newTOTPFixture(t)
	fixture.enrol(t)
	fixture.clock.advance(stepsAfterEnrolment * totpPeriod)
	return fixture
}

func totpCodeAt(t *testing.T, secret string, step int64) string {
	t.Helper()
	code, err := hotp.GenerateCodeCustom(secret, uint64(step), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	if err != nil {
		t.Fatal(err)
	}
	return code
}

func (fixture *totpFixture) currentStep() int64 {
	return totpStepAt(fixture.clock.now())
}

func (fixture *totpFixture) codeAtOffset(t *testing.T, offset int64) string {
	t.Helper()
	return totpCodeAt(t, fixture.secret, fixture.currentStep()+offset)
}

func (fixture *totpFixture) begin(t *testing.T) TOTPEnrolment {
	t.Helper()
	enrolment, err := fixture.service.BeginTOTPSetup(context.Background(), fixture.grant.Token, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return enrolment
}

func (fixture *totpFixture) enrol(t *testing.T) {
	t.Helper()
	enrolment := fixture.begin(t)
	code := totpCodeAt(t, enrolment.SecretBase32, fixture.currentStep())
	if err := fixture.service.FinishTOTPSetup(context.Background(), fixture.grant.Token, enrolment.State, code); err != nil {
		t.Fatal(err)
	}
	fixture.secret = enrolment.SecretBase32
}

func (fixture *totpFixture) ticket(t *testing.T) string {
	t.Helper()
	result, err := fixture.service.Login(context.Background(), testUsername, testPassword, testClient)
	if err != nil || result.Ticket == "" || result.Session != nil {
		t.Fatalf("valeurAttendue un ticket sans session, valeurObtenue %+v err=%v", result, err)
	}
	return result.Ticket
}

func (fixture *totpFixture) storedFactor(t *testing.T) (store.TOTPFactor, bool) {
	t.Helper()
	factor, found, err := fixture.store.TOTP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return factor, found
}

func (fixture *totpFixture) sessionCount(t *testing.T) int {
	t.Helper()
	sessions, err := fixture.store.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return len(sessions)
}

func expectInvalidCode(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrInvalidCode) || err.Error() != ErrInvalidCode.Error() {
		t.Fatalf("valeurAttendue exactement ErrInvalidCode, valeurObtenue %v", err)
	}
}

func expectThrottledFor(t *testing.T, err error, valeurAttendue time.Duration) {
	t.Helper()
	var throttled *ThrottledError
	if !errors.As(err, &throttled) || throttled.RetryAfter != valeurAttendue {
		t.Fatalf("valeurAttendue un délai de %v, valeurObtenue %v", valeurAttendue, err)
	}
}

func TestTOTPSetup_requiresAValidCodeToConfirmThenStoresASealedSecret(t *testing.T) {
	fixture := newTOTPFixture(t)
	ctx := context.Background()
	enrolment := fixture.begin(t)

	validURI := strings.HasPrefix(enrolment.OtpauthURI, "otpauth://totp/Necromanciarr:"+testUsername) &&
		strings.Contains(enrolment.OtpauthURI, "secret="+enrolment.SecretBase32)
	if !validURI || len(enrolment.SecretBase32) != 32 || !bytes.HasPrefix(enrolment.QRPNG, pngSignature) || len(enrolment.State) == 0 {
		t.Fatalf("valeurAttendue URI, secret de 20 octets, QR PNG et état, valeurObtenue %+v", enrolment)
	}
	wrongCode := totpCodeAt(t, enrolment.SecretBase32, fixture.currentStep()+wrongStepOffset)
	expectInvalidCode(t, fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, wrongCode))
	if _, found := fixture.storedFactor(t); found {
		t.Fatal("valeurAttendue aucun TOTP après un code faux, valeurObtenue un TOTP enregistré")
	}

	enrolment = fixture.begin(t)
	code := totpCodeAt(t, enrolment.SecretBase32, fixture.currentStep())
	if err := fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, code); err != nil {
		t.Fatal(err)
	}

	factor, found := fixture.storedFactor(t)
	sealer, err := config.NewSecretSealer(testSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	valeurObtenue, openErr := sealer.OpenSetting(totpSealKey, string(factor.SecretSealed))
	if !found || openErr != nil || valeurObtenue != enrolment.SecretBase32 {
		t.Fatalf("valeurAttendue le secret scellé avec la clé, valeurObtenue found=%v err=%v", found, openErr)
	}
	summary, err := fixture.service.AccountSummary(ctx)
	if err != nil || !summary.TOTP {
		t.Fatalf("valeurAttendue TOTP: true, valeurObtenue %+v err=%v", summary, err)
	}
}

func TestTOTPSetup_refusesWithoutASecretKey(t *testing.T) {
	fixture := newTOTPFixture(t)
	ctx := context.Background()
	enrolment := fixture.begin(t)
	code := totpCodeAt(t, enrolment.SecretBase32, fixture.currentStep())
	fixture.service.deps.Config = config.Config{}

	_, beginErr := fixture.service.BeginTOTPSetup(ctx, fixture.grant.Token, testPassword)
	finishErr := fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, code)
	_, loginErr := fixture.service.LoginWithTOTP(ctx, "any-ticket", code, testClient)

	for _, valeurObtenue := range []error{beginErr, finishErr, loginErr} {
		if !errors.Is(valeurObtenue, ErrTOTPUnavailable) {
			t.Fatalf("valeurAttendue ErrTOTPUnavailable, valeurObtenue %v", valeurObtenue)
		}
	}
	if _, found := fixture.storedFactor(t); found {
		t.Fatal("valeurAttendue aucun TOTP sans clé, valeurObtenue un TOTP enregistré")
	}
}

func TestTOTPLogin_acceptsThePreviousCurrentAndNextStepsOnly(t *testing.T) {
	expectations := []struct {
		offset         int64
		valeurAttendue bool
	}{{-2, false}, {-1, true}, {0, true}, {1, true}, {2, false}}
	for _, expectation := range expectations {
		fixture := newEnrolledTOTPFixture(t)
		ticket := fixture.ticket(t)

		grant, err := fixture.service.LoginWithTOTP(context.Background(), ticket, fixture.codeAtOffset(t, expectation.offset), testClient)

		if valeurObtenue := err == nil && grant.Token != ""; valeurObtenue != expectation.valeurAttendue {
			t.Fatalf("pas %+d : valeurAttendue accepté=%v, valeurObtenue %v", expectation.offset, expectation.valeurAttendue, err)
		}
		if !expectation.valeurAttendue {
			expectInvalidCode(t, err)
		}
	}
}

func TestTOTPLogin_refusesAReplayOfTheSameStep(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	ctx := context.Background()
	code := fixture.codeAtOffset(t, 0)
	if _, err := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), code, testClient); err != nil {
		t.Fatal(err)
	}

	_, replayErr := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), code, testClient)
	expectInvalidCode(t, replayErr)
	fixture.clock.advance(time.Second)
	_, olderErr := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), fixture.codeAtOffset(t, -1), testClient)
	expectInvalidCode(t, olderErr)
}

func TestTOTPLogin_refusesAReplayOfTheSameStepUnderConcurrentPresentations(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	ctx := context.Background()
	tickets := make([]string, concurrentAttempts)
	for ticketIndex := range tickets {
		tickets[ticketIndex] = fixture.ticket(t)
	}
	sessionsBefore := fixture.sessionCount(t)
	code := fixture.codeAtOffset(t, 1)
	grants := runConcurrently(func(attemptIndex int) error {
		_, err := fixture.service.LoginWithTOTP(ctx, tickets[attemptIndex], code, testClient)
		return err
	})
	if valeurObtenue := fixture.sessionCount(t) - sessionsBefore; grants != 1 || valeurObtenue != 1 {
		t.Fatalf("valeurAttendue une seule session, valeurObtenue %d succès, %d sessions", grants, valeurObtenue)
	}

	fixture.clock.advance(totpPeriod)
	sealer, err := fixture.service.totpSealer()
	if err != nil {
		t.Fatal(err)
	}
	nextCode := fixture.codeAtOffset(t, 1)
	accepted := runConcurrently(func(int) error { return fixture.service.acceptTOTPCode(ctx, sealer, nextCode) })

	if valeurAttendue := 1; accepted != valeurAttendue {
		t.Fatalf("valeurAttendue %d pas accepté hors limiteur, valeurObtenue %d", valeurAttendue, accepted)
	}
}

// runConcurrently lance concurrentAttempts appels en même temps et compte ceux qui réussissent.
func runConcurrently(attempt func(attemptIndex int) error) int {
	var successes sync.WaitGroup
	results := make([]error, concurrentAttempts)
	start := make(chan struct{})
	for attemptIndex := range results {
		successes.Go(func() {
			<-start
			results[attemptIndex] = attempt(attemptIndex)
		})
	}
	close(start)
	successes.Wait()
	count := 0
	for _, result := range results {
		if result == nil {
			count++
		}
	}
	return count
}

func TestTOTPLogin_failuresShareTheThrottleWithThePassword(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	ctx := context.Background()
	wrongCode := fixture.codeAtOffset(t, wrongStepOffset)
	_, err := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), wrongCode, testClient)
	expectInvalidCode(t, err)
	_, err = fixture.service.Login(ctx, testUsername, testPassword, testClient)
	expectThrottledFor(t, err, time.Second)

	fixture.clock.advance(time.Second)
	failingTicket, laterTicket := fixture.ticket(t), fixture.ticket(t)
	_, err = fixture.service.LoginWithTOTP(ctx, failingTicket, wrongCode, testClient)
	expectInvalidCode(t, err)

	_, err = fixture.service.LoginWithTOTP(ctx, laterTicket, fixture.codeAtOffset(t, 0), testClient)
	expectThrottledFor(t, err, 2*time.Second)
	fixture.clock.advance(time.Second)
	_, err = fixture.service.LoginWithTOTP(ctx, laterTicket, fixture.codeAtOffset(t, 0), testClient)
	expectThrottledFor(t, err, time.Second)
	fixture.clock.advance(time.Second)
	grant, err := fixture.service.LoginWithTOTP(ctx, laterTicket, fixture.codeAtOffset(t, 0), testClient)

	if err != nil || grant.Token == "" {
		t.Fatalf("valeurAttendue une session une fois le délai écoulé, valeurObtenue %v", err)
	}
}

func TestTOTPLogin_requiresAFreshTicket(t *testing.T) {
	ctx := context.Background()
	t.Run("absent ou inconnu", func(t *testing.T) {
		fixture := newEnrolledTOTPFixture(t)
		for _, ticket := range []string{"", "unknown-ticket"} {
			fixture.passThrottleDelay()
			_, err := fixture.service.LoginWithTOTP(ctx, ticket, fixture.codeAtOffset(t, 0), testClient)
			if !errors.Is(err, ErrLoginTicketInvalid) {
				t.Fatalf("valeurAttendue ErrLoginTicketInvalid, valeurObtenue %v", err)
			}
		}
	})
	t.Run("expiré", func(t *testing.T) {
		fixture := newEnrolledTOTPFixture(t)
		ticket := fixture.ticket(t)
		fixture.clock.advance(loginTicketLifetime)
		_, err := fixture.service.LoginWithTOTP(ctx, ticket, fixture.codeAtOffset(t, 0), testClient)
		if !errors.Is(err, ErrLoginTicketInvalid) {
			t.Fatalf("valeurAttendue ErrLoginTicketInvalid, valeurObtenue %v", err)
		}
	})
	t.Run("réutilisé après un succès", func(t *testing.T) {
		fixture := newEnrolledTOTPFixture(t)
		ticket := fixture.ticket(t)
		if _, err := fixture.service.LoginWithTOTP(ctx, ticket, fixture.codeAtOffset(t, 0), testClient); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.service.LoginWithTOTP(ctx, ticket, fixture.codeAtOffset(t, 1), testClient)
		if !errors.Is(err, ErrLoginTicketInvalid) {
			t.Fatalf("valeurAttendue ErrLoginTicketInvalid, valeurObtenue %v", err)
		}
	})
	t.Run("autre génération du compte", func(t *testing.T) {
		fixture := newEnrolledTOTPFixture(t)
		ticket := fixture.ticket(t)
		if err := fixture.service.ChangePassword(ctx, fixture.grant.Token, testPassword, testNewPassword); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.service.LoginWithTOTP(ctx, ticket, fixture.codeAtOffset(t, 0), testClient)
		if !errors.Is(err, ErrLoginTicketInvalid) {
			t.Fatalf("valeurAttendue ErrLoginTicketInvalid, valeurObtenue %v", err)
		}
	})
	t.Run("brûlé par un code faux ou mal formé", func(t *testing.T) {
		fixture := newEnrolledTOTPFixture(t)
		for _, badCode := range []string{fixture.codeAtOffset(t, wrongStepOffset), "12"} {
			ticket := fixture.ticket(t)
			_, err := fixture.service.LoginWithTOTP(ctx, ticket, badCode, testClient)
			expectInvalidCode(t, err)
			fixture.passThrottleDelay()
			_, err = fixture.service.LoginWithTOTP(ctx, ticket, fixture.codeAtOffset(t, 0), testClient)
			if !errors.Is(err, ErrLoginTicketInvalid) {
				t.Fatalf("valeurAttendue ErrLoginTicketInvalid après un échec, valeurObtenue %v", err)
			}
			fixture.passThrottleDelay()
		}
	})
	t.Run("gardé par un essai refusé par le limiteur", func(t *testing.T) {
		fixture := newEnrolledTOTPFixture(t)
		failingTicket, keptTicket := fixture.ticket(t), fixture.ticket(t)
		_, err := fixture.service.LoginWithTOTP(ctx, failingTicket, fixture.codeAtOffset(t, wrongStepOffset), testClient)
		expectInvalidCode(t, err)
		_, err = fixture.service.LoginWithTOTP(ctx, keptTicket, fixture.codeAtOffset(t, 0), testClient)
		expectThrottledFor(t, err, time.Second)
		fixture.clock.advance(time.Second)
		if _, err := fixture.service.LoginWithTOTP(ctx, keptTicket, fixture.codeAtOffset(t, 0), testClient); err != nil {
			t.Fatalf("valeurAttendue le ticket encore valable, valeurObtenue %v", err)
		}
	})
}

func TestTOTPLogin_acceptsACodeWithOneSeparator(t *testing.T) {
	for _, separator := range []string{" ", "-"} {
		fixture := newEnrolledTOTPFixture(t)
		code := fixture.codeAtOffset(t, 0)
		typed := "  " + code[:3] + separator + code[3:] + " "
		if _, err := fixture.service.LoginWithTOTP(context.Background(), fixture.ticket(t), typed, testClient); err != nil {
			t.Fatalf("valeurAttendue %q accepté, valeurObtenue %v", typed, err)
		}
	}
	for _, malformed := range []string{"1234567", "12 34 56", "12--3456", "１２３４５６", "abcdef", "-123456", "123456-", ""} {
		if _, valid := normalizeTOTPCode(malformed); valid {
			t.Fatalf("valeurAttendue %q refusé, valeurObtenue accepté", malformed)
		}
	}
}

func TestTOTPLogin_neverLogsOrReturnsTheCodeOrTheSecret(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	var logged bytes.Buffer
	fixture.service.deps.Logger = slog.New(slog.NewTextHandler(&logged, nil))
	ctx := context.Background()
	validCode, wrongCode := fixture.codeAtOffset(t, 0), fixture.codeAtOffset(t, wrongStepOffset)
	if _, err := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), validCode, testClient); err != nil {
		t.Fatal(err)
	}
	var refusals []error
	for _, presented := range []string{validCode, wrongCode, "12 3456x"} {
		fixture.passThrottleDelay()
		_, err := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), presented, testClient)
		refusals = append(refusals, err)
	}
	fixture.passThrottleDelay()
	enrolment := fixture.begin(t)
	refusals = append(refusals, fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, wrongCode))

	for _, refusal := range refusals {
		expectInvalidCode(t, refusal)
	}
	for _, sensitive := range []string{validCode, wrongCode, fixture.secret, enrolment.SecretBase32} {
		if strings.Contains(logged.String(), sensitive) {
			t.Fatal("valeurAttendue aucun code ni secret dans le journal, valeurObtenue une fuite")
		}
	}
}

func TestRemoveTOTP_requiresThePasswordAndReturnsLoginToPasswordOnly(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	ctx := context.Background()

	if err := fixture.service.RemoveTOTP(ctx, "unknown-session", testPassword); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	if err := fixture.service.RemoveTOTP(ctx, fixture.grant.Token, testWrong); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("valeurAttendue ErrInvalidCredentials, valeurObtenue %v", err)
	}
	if _, found := fixture.storedFactor(t); !found {
		t.Fatal("valeurAttendue le TOTP gardé après un mot de passe faux, valeurObtenue supprimé")
	}
	fixture.passThrottleDelay()
	if err := fixture.service.RemoveTOTP(ctx, fixture.grant.Token, testPassword); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || result.Session == nil || len(result.SecondFactors) != 0 {
		t.Fatalf("valeurAttendue une session au mot de passe seul, valeurObtenue %+v err=%v", result, err)
	}
}

func TestTOTPSecret_isNeverStoredInClear(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	factor, found := fixture.storedFactor(t)
	rawSecret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(fixture.secret)
	if err != nil {
		t.Fatal(err)
	}

	inClear := bytes.Contains(factor.SecretSealed, []byte(fixture.secret)) ||
		bytes.Contains(bytes.ToUpper(factor.SecretSealed), []byte(fixture.secret)) || bytes.Contains(factor.SecretSealed, rawSecret)
	if !found || inClear || !bytes.HasPrefix(factor.SecretSealed, []byte("sealed:v1:")) {
		t.Fatalf("valeurAttendue un secret scellé seulement, valeurObtenue found=%v enClair=%v", found, inClear)
	}
}

func TestTOTPSetupState_neverCarriesThePendingSecretInClear(t *testing.T) {
	fixture := newTOTPFixture(t)
	enrolment := fixture.begin(t)

	payload, err := fixture.service.unsealState(enrolment.State)

	if err != nil || bytes.Contains(payload, []byte(enrolment.SecretBase32)) {
		t.Fatalf("valeurAttendue un état sans le secret en clair, valeurObtenue err=%v", err)
	}
}

func TestTOTPSetup_seedsLastStepSoTheEnrolmentCodeCannotBeReplayed(t *testing.T) {
	fixture := newTOTPFixture(t)
	ctx := context.Background()
	enrolment := fixture.begin(t)
	enrolmentStep := fixture.currentStep() + 1
	if err := fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, totpCodeAt(t, enrolment.SecretBase32, enrolmentStep)); err != nil {
		t.Fatal(err)
	}
	fixture.secret = enrolment.SecretBase32

	if factor, _ := fixture.storedFactor(t); factor.LastStep != enrolmentStep {
		t.Fatalf("valeurAttendue %d, valeurObtenue %d", enrolmentStep, factor.LastStep)
	}
	for _, offset := range []int64{1, 0} {
		_, err := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), fixture.codeAtOffset(t, offset), testClient)
		expectInvalidCode(t, err)
		fixture.clock.advance(time.Second)
	}
}

func TestTOTPSetupState_cannotBeUsedForAnotherCeremony(t *testing.T) {
	fixture := newWebAuthnFixture(t)
	fixture.service.deps.Config.SecretKey = testSecretKey
	ctx := context.Background()
	key := newVirtualKey(virtualwebauthn.AuthenticatorOptions{})
	fixture.register(t, key, credentialKindSecondFactor)
	totp := &totpFixture{accountFixture: fixture.accountFixture, grant: fixture.grant}
	totpState := totp.begin(t).State
	ticket := fixture.loginTicket(t)

	registrationErr := fixture.service.FinishWebAuthnRegistration(ctx, fixture.grant.Token, totpState, testKeyName, strings.NewReader("{}"))
	_, passkeyErr := fixture.service.FinishPasskeyLogin(ctx, totpState, strings.NewReader("{}"), testClient)
	fixture.passThrottleDelay()
	_, secondFactorErr := fixture.service.FinishWebAuthnLogin(ctx, ticket, totpState, strings.NewReader("{}"), testClient)
	for _, refusal := range []error{registrationErr, passkeyErr, secondFactorErr} {
		expectInvalidAssertion(t, refusal)
	}

	fixture.passThrottleDelay()
	registrationState := fixture.beginRegistration(t, credentialKindPasskey).State
	passkeyCeremony, err := fixture.service.BeginPasskeyLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	anyCode := totpCodeAt(t, "JBSWY3DPEHPK3PXP", totp.currentStep())
	for _, foreignState := range [][]byte{registrationState, passkeyCeremony.State} {
		expectInvalidCode(t, fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, foreignState, anyCode))
	}
}

func TestTOTPSetupState_isBoundToItsSessionAndUsableOnce(t *testing.T) {
	fixture := newTOTPFixture(t)
	ctx := context.Background()
	enrolment := fixture.begin(t)
	code := totpCodeAt(t, enrolment.SecretBase32, fixture.currentStep())
	otherSession := fixture.setupSecondSession(t)

	expectInvalidCode(t, fixture.service.FinishTOTPSetup(ctx, otherSession.Token, enrolment.State, code))

	enrolment = fixture.begin(t)
	code = totpCodeAt(t, enrolment.SecretBase32, fixture.currentStep())
	if err := fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, code); err != nil {
		t.Fatal(err)
	}
	expectInvalidCode(t, fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, code))
	fixture.clock.advance(totpSetupLifetime)
	expired := fixture.begin(t)
	fixture.clock.advance(totpSetupLifetime)
	expectInvalidCode(t, fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, expired.State, totpCodeAt(t, expired.SecretBase32, fixture.currentStep())))
}

func (fixture *totpFixture) setupSecondSession(t *testing.T) SessionGrant {
	t.Helper()
	result, err := fixture.service.Login(context.Background(), testUsername, testPassword, testClient)
	if err != nil || result.Session == nil {
		t.Fatalf("valeurAttendue une seconde session, valeurObtenue %+v err=%v", result, err)
	}
	return *result.Session
}

func TestNew_minimalDepsEnrolAndSignInWithTOTP(t *testing.T) {
	service := New(Deps{Store: openTestStore(t), Config: testTOTPSettings})
	ctx := context.Background()
	grant, err := service.SetupAccount(ctx, testUsername, testPassword, testClient)
	if err != nil {
		t.Fatal(err)
	}
	enrolment, err := service.BeginTOTPSetup(ctx, grant.Token, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.FinishTOTPSetup(ctx, grant.Token, enrolment.State, totpCodeAt(t, enrolment.SecretBase32, totpStepAt(time.Now()))); err != nil {
		t.Fatal(err)
	}
	login, err := service.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || login.Ticket == "" {
		t.Fatalf("valeurAttendue un ticket, valeurObtenue %+v err=%v", login, err)
	}

	nextCode := totpCodeAt(t, enrolment.SecretBase32, totpStepAt(time.Now())+1)
	session, err := service.LoginWithTOTP(ctx, login.Ticket, nextCode, testClient)

	if err != nil || session.Token == "" {
		t.Fatalf("valeurAttendue une session, valeurObtenue %+v err=%v", session, err)
	}
}

func TestTOTPLogin_anUnreadableSecretIsAnInternalFaultThatCountsNoFailure(t *testing.T) {
	ctx := context.Background()
	makeUnreadable := map[string]func(t *testing.T, fixture *totpFixture){
		"clé remplacée": func(*testing.T, *totpFixture) {},
		"ligne corrompue": func(t *testing.T, fixture *totpFixture) {
			factor, _ := fixture.storedFactor(t)
			factor.SecretSealed = []byte("sealed:v1:corrupted")
			if err := fixture.store.SetTOTP(ctx, factor); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range makeUnreadable {
		t.Run(name, func(t *testing.T) {
			fixture := newEnrolledTOTPFixture(t)
			corrupt(t, fixture)
			if name == "clé remplacée" {
				fixture.service.deps.Config.SecretKey = bytes.Repeat([]byte{0x08}, config.SecretKeySize)
			}
			ticket := fixture.ticket(t)
			sessionsBefore := fixture.sessionCount(t)
			code := fixture.codeAtOffset(t, 0)

			_, err := fixture.service.LoginWithTOTP(ctx, ticket, code, testClient)

			leaks := strings.Contains(err.Error(), code) || strings.Contains(err.Error(), fixture.secret)
			if !errors.Is(err, ErrTOTPSecretUnreadable) || errors.Is(err, ErrInvalidCode) || leaks {
				t.Fatalf("valeurAttendue ErrTOTPSecretUnreadable sans code ni secret, valeurObtenue %v", err)
			}
			if valeurObtenue := fixture.sessionCount(t) - sessionsBefore; valeurObtenue != 0 {
				t.Fatalf("valeurAttendue aucune session, valeurObtenue %d", valeurObtenue)
			}
			if result, err := fixture.service.Login(ctx, testUsername, testPassword, testClient); err != nil || result.Ticket == "" {
				t.Fatalf("valeurAttendue un limiteur non incrémenté, valeurObtenue %+v err=%v", result, err)
			}
			_, err = fixture.service.LoginWithTOTP(ctx, ticket, code, testClient)
			if !errors.Is(err, ErrLoginTicketInvalid) {
				t.Fatalf("valeurAttendue le ticket brûlé, valeurObtenue %v", err)
			}
		})
	}
}

func TestBeginTOTPSetup_requiresASessionAndThePassword(t *testing.T) {
	fixture := newTOTPFixture(t)
	ctx := context.Background()

	if _, err := fixture.service.BeginTOTPSetup(ctx, "unknown-session", testPassword); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("valeurAttendue ErrSessionInvalid, valeurObtenue %v", err)
	}
	enrolment, err := fixture.service.BeginTOTPSetup(ctx, fixture.grant.Token, testWrong)
	if !errors.Is(err, ErrInvalidCredentials) || len(enrolment.State) != 0 || enrolment.SecretBase32 != "" {
		t.Fatalf("valeurAttendue ErrInvalidCredentials sans enrôlement, valeurObtenue %+v err=%v", enrolment, err)
	}
	if _, found := fixture.storedFactor(t); found {
		t.Fatal("valeurAttendue aucun TOTP, valeurObtenue un TOTP enregistré")
	}

	_, err = fixture.service.Login(ctx, testUsername, testPassword, testClient)
	expectThrottledFor(t, err, time.Second)
}

func TestTOTPSetup_reEnrolmentAfterThePasswordReplacesTheSecret(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	ctx := context.Background()
	oldSecret := fixture.secret
	enrolment := fixture.begin(t)
	newStep := fixture.currentStep() + 1
	if err := fixture.service.FinishTOTPSetup(ctx, fixture.grant.Token, enrolment.State, totpCodeAt(t, enrolment.SecretBase32, newStep)); err != nil {
		t.Fatal(err)
	}
	if factor, _ := fixture.storedFactor(t); factor.LastStep != newStep {
		t.Fatalf("valeurAttendue %d, valeurObtenue %d", newStep, factor.LastStep)
	}

	fixture.clock.advance(totpPeriod)
	_, err := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), totpCodeAt(t, oldSecret, fixture.currentStep()+1), testClient)
	expectInvalidCode(t, err)
	fixture.clock.advance(time.Second)
	grant, err := fixture.service.LoginWithTOTP(ctx, fixture.ticket(t), totpCodeAt(t, enrolment.SecretBase32, fixture.currentStep()+1), testClient)

	if err != nil || grant.Token == "" {
		t.Fatalf("valeurAttendue une session avec le nouveau secret, valeurObtenue %v", err)
	}
}

// reEnrolDuringLoginStore remplace le secret juste après la lecture du facteur, entre la vérification du code et
// l'avance du pas.
type reEnrolDuringLoginStore struct {
	*store.Store
	armed          atomic.Bool
	replacedSealed []byte
}

func (interleaving *reEnrolDuringLoginStore) TOTP(ctx context.Context) (store.TOTPFactor, bool, error) {
	factor, found, err := interleaving.Store.TOTP(ctx)
	if err == nil && found && interleaving.armed.CompareAndSwap(true, false) {
		replaced := factor
		replaced.SecretSealed = interleaving.replacedSealed
		if setErr := interleaving.SetTOTP(ctx, replaced); setErr != nil {
			return store.TOTPFactor{}, false, setErr
		}
	}
	return factor, found, err
}

func TestTOTPLogin_aReEnrolmentBetweenVerificationAndStepAdvanceRefusesTheOldCode(t *testing.T) {
	interleaving := &reEnrolDuringLoginStore{Store: openTestStore(t)}
	accountFixture := newAccountFixtureOn(t, interleaving)
	accountFixture.store = interleaving.Store
	accountFixture.service.deps.Config = testTOTPSettings
	fixture := &totpFixture{accountFixture: accountFixture, grant: accountFixture.setup(t)}
	fixture.enrol(t)
	fixture.clock.advance(stepsAfterEnrolment * totpPeriod)
	sealer, err := config.NewSecretSealer(testSecretKey)
	if err != nil {
		t.Fatal(err)
	}
	replacedSealed, err := sealer.SealSetting(totpSealKey, "JBSWY3DPEHPK3PXP")
	if err != nil {
		t.Fatal(err)
	}
	interleaving.replacedSealed = []byte(replacedSealed)
	ticket := fixture.ticket(t)
	interleaving.armed.Store(true)

	_, err = fixture.service.LoginWithTOTP(context.Background(), ticket, fixture.codeAtOffset(t, 0), testClient)

	expectInvalidCode(t, err)
	if factor, _ := fixture.storedFactor(t); factor.LastStep == fixture.currentStep() {
		t.Fatal("valeurAttendue le pas du nouveau secret inchangé, valeurObtenue avancé par un code de l'ancien")
	}
}

func TestTOTP_aFreshServiceRefusesAnEarlierSetupStateButReadsTheStoredFactor(t *testing.T) {
	fixture := newEnrolledTOTPFixture(t)
	ctx := context.Background()
	earlierState := fixture.begin(t)
	restarted := New(Deps{Store: fixture.store, Config: testTOTPSettings, Now: fixture.clock.now})

	finishErr := restarted.FinishTOTPSetup(ctx, fixture.grant.Token, earlierState.State, totpCodeAt(t, earlierState.SecretBase32, fixture.currentStep()))
	expectInvalidCode(t, finishErr)
	login, err := restarted.Login(ctx, testUsername, testPassword, testClient)
	if err != nil || login.Ticket == "" {
		t.Fatalf("valeurAttendue un ticket, valeurObtenue %+v err=%v", login, err)
	}
	grant, err := restarted.LoginWithTOTP(ctx, login.Ticket, fixture.codeAtOffset(t, 0), testClient)

	if err != nil || grant.Token == "" {
		t.Fatalf("valeurAttendue une session avec le facteur enregistré, valeurObtenue %v", err)
	}
}

func TestHighestMatchingStep_anEpochClockSkipsTheNegativeCandidateAndMatchesStepZero(t *testing.T) {
	epoch := time.Unix(0, 0)
	const secret = "JBSWY3DPEHPK3PXP"
	expectations := []struct {
		step               int64
		pasAttendu         int64
		correspondAttendue bool
	}{{0, 0, true}, {1, 1, true}, {2, 0, false}}
	for _, expectation := range expectations {
		pasObtenu, correspondObtenue := highestMatchingStep(secret, totpCodeAt(t, secret, expectation.step), epoch)

		if pasObtenu != expectation.pasAttendu || correspondObtenue != expectation.correspondAttendue {
			t.Fatalf("pas %d : valeurAttendue (%d,%v), valeurObtenue (%d,%v)", expectation.step, expectation.pasAttendu, expectation.correspondAttendue, pasObtenu, correspondObtenue)
		}
	}
}
