package web

import (
	"context"
	"io"
	"time"

	"necromanciarr/internal/app"
)

// Valeurs de test du compte simulé (faible entropie, gitleaks).
const (
	fakeUsername     = "alice"
	fakeSessionToken = "fake-session-token"
	fakeTicket       = "fake-login-ticket"
)

// fakeAccount simule le compte : exists bascule le mode, sessions liste les jetons valables ; loginResult, ceremony
// et enrolment sont rendus tels quels ; le dernier état reçu par chaque finish est gardé.
type fakeAccount struct {
	exists            bool
	webAuthnAvailable bool
	sessions          map[string]bool
	loginResult       app.LoginResult
	ceremony          app.WebAuthnCeremony
	enrolment         app.TOTPEnrolment
	summary           app.AccountSummary
	lastState         []byte
	lastTicket        string
	lastBody          string
}

func newFakeGrant() app.SessionGrant {
	return app.SessionGrant{Token: fakeSessionToken, ExpiresAt: time.Now().Add(time.Hour)}
}

// withAccount passe le faux service en mode compte, avec une session ouverte sous fakeSessionToken.
func (fake *fakeService) withAccount() *fakeService {
	fake.account.exists = true
	fake.account.sessions = map[string]bool{fakeSessionToken: true}
	fake.account.loginResult = app.LoginResult{Session: new(newFakeGrant())}
	return fake
}

func (fake *fakeService) AuthStatus(context.Context) (app.AuthStatus, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return app.AuthStatus{AccountExists: fake.account.exists, WebAuthnAvailable: fake.account.webAuthnAvailable}, fake.errors["AuthStatus"]
}

func (fake *fakeService) SetupAccount(context.Context, string, string, app.ClientInfo) (app.SessionGrant, error) {
	if err := fake.record("SetupAccount"); err != nil {
		return app.SessionGrant{}, err
	}
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if fake.account.exists {
		return app.SessionGrant{}, app.ErrAccountExists
	}
	fake.account.exists = true
	fake.account.sessions = map[string]bool{fakeSessionToken: true}
	return newFakeGrant(), nil
}

func (fake *fakeService) Login(context.Context, string, string, app.ClientInfo) (app.LoginResult, error) {
	if err := fake.record("Login"); err != nil {
		return app.LoginResult{}, err
	}
	return fake.account.loginResult, nil
}

func (fake *fakeService) ResumeSession(_ context.Context, token string) (app.SessionView, error) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if !fake.account.sessions[token] {
		return app.SessionView{}, app.ErrSessionInvalid
	}
	return app.SessionView{Username: fakeUsername, Current: true}, nil
}

func (fake *fakeService) Logout(_ context.Context, token string) error {
	err := fake.record("Logout")
	fake.mutex.Lock()
	delete(fake.account.sessions, token)
	fake.mutex.Unlock()
	return err
}

func (fake *fakeService) LogoutAll(context.Context) error {
	err := fake.record("LogoutAll")
	fake.mutex.Lock()
	fake.account.sessions = map[string]bool{}
	fake.mutex.Unlock()
	return err
}

func (fake *fakeService) ChangePassword(context.Context, string, string, string) error {
	return fake.record("ChangePassword")
}

func (fake *fakeService) ListSessions(context.Context, string) ([]app.SessionView, error) {
	return []app.SessionView{{Username: fakeUsername, Current: true, Address: "127.0.0.1:1"}}, fake.record("ListSessions")
}

func (fake *fakeService) ResetAccount(context.Context) error {
	err := fake.record("ResetAccount")
	fake.mutex.Lock()
	fake.account.exists = false
	fake.mutex.Unlock()
	return err
}

func (fake *fakeService) AccountSummary(context.Context) (app.AccountSummary, error) {
	return fake.account.summary, nil
}

func (fake *fakeService) BeginWebAuthnRegistration(_ context.Context, _, _, kind string) (app.WebAuthnCeremony, error) {
	return fake.account.ceremony, fake.record("BeginWebAuthnRegistration", kind)
}

func (fake *fakeService) FinishWebAuthnRegistration(_ context.Context, _ string, state []byte, name string, body io.Reader) error {
	fake.keepFinish(state, "", body)
	return fake.record("FinishWebAuthnRegistration", name)
}

func (fake *fakeService) BeginWebAuthnLogin(_ context.Context, ticket string) (app.WebAuthnCeremony, error) {
	return fake.account.ceremony, fake.record("BeginWebAuthnLogin", ticket)
}

func (fake *fakeService) FinishWebAuthnLogin(_ context.Context, ticket string, state []byte, body io.Reader, _ app.ClientInfo) (app.SessionGrant, error) {
	fake.keepFinish(state, ticket, body)
	return newFakeGrant(), fake.record("FinishWebAuthnLogin")
}

func (fake *fakeService) BeginPasskeyLogin(context.Context) (app.WebAuthnCeremony, error) {
	return fake.account.ceremony, fake.record("BeginPasskeyLogin")
}

func (fake *fakeService) FinishPasskeyLogin(_ context.Context, state []byte, body io.Reader, _ app.ClientInfo) (app.SessionGrant, error) {
	fake.keepFinish(state, "", body)
	return newFakeGrant(), fake.record("FinishPasskeyLogin")
}

func (fake *fakeService) ListCredentials(context.Context) ([]app.CredentialView, error) {
	return nil, fake.record("ListCredentials")
}

func (fake *fakeService) RenameCredential(_ context.Context, id, name string) error {
	return fake.record("RenameCredential", id, " ", name)
}

func (fake *fakeService) DeleteCredential(_ context.Context, _, id, _ string) error {
	return fake.record("DeleteCredential", id)
}

func (fake *fakeService) BeginTOTPSetup(context.Context, string, string) (app.TOTPEnrolment, error) {
	return fake.account.enrolment, fake.record("BeginTOTPSetup")
}

func (fake *fakeService) FinishTOTPSetup(_ context.Context, _ string, state []byte, _ string) error {
	fake.keepFinish(state, "", nil)
	return fake.record("FinishTOTPSetup")
}

func (fake *fakeService) LoginWithTOTP(_ context.Context, ticket, _ string, _ app.ClientInfo) (app.SessionGrant, error) {
	fake.keepFinish(nil, ticket, nil)
	return newFakeGrant(), fake.record("LoginWithTOTP")
}

func (fake *fakeService) RemoveTOTP(context.Context, string, string) error {
	return fake.record("RemoveTOTP")
}

func (fake *fakeService) keepFinish(state []byte, ticket string, body io.Reader) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.account.lastState, fake.account.lastTicket = state, ticket
	if body != nil {
		read, _ := io.ReadAll(body)
		fake.account.lastBody = string(read)
	}
}

func (fake *fakeService) callCount(method string) int {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	count := 0
	for _, recorded := range fake.calls {
		if recorded == method || len(recorded) > len(method) && recorded[:len(method)+1] == method+" " {
			count++
		}
	}
	return count
}
