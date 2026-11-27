package web

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"necromanciarr/internal/app"
	"necromanciarr/internal/auth"
)

const pathCredentialID = "id"

type passwordBody struct {
	Password string `json:"password"`
}

type changePasswordBody struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

type registrationBeginBody struct {
	Password string `json:"password"`
	Kind     string `json:"kind"`
}

type registrationFinishBody struct {
	Name       string          `json:"name"`
	Credential json.RawMessage `json:"credential"`
}

type nameBody struct {
	Name string `json:"name"`
}

type ceremonyBody struct {
	WebAuthn json.RawMessage `json:"webauthn"`
}

// totpEnrolmentBody est montré une fois à l'opérateur connecté ; QRPNGBase64 est nul si le Service n'a pas rendu d'image.
type totpEnrolmentBody struct {
	OtpauthURI  string  `json:"otpauthUri"`
	Secret      string  `json:"secret"`
	QRPNGBase64 *string `json:"qrPngBase64"`
}

type sessionItem struct {
	Current    bool      `json:"current"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	Address    string    `json:"address"`
}

type credentialItem struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

// decodePassword lit le mot de passe ressaisi ; au-delà de la borne il ne peut pas être juste.
func decodePassword(request *http.Request) (string, error) {
	var body passwordBody
	if err := decodeBody(request, &body); err != nil {
		return "", err
	}
	if len(body.Password) > auth.MaxPasswordBytes {
		return "", app.ErrInvalidCredentials
	}
	return body.Password, nil
}

func (handler *Handler) sessionCeremonyKey(request *http.Request, purpose string) string {
	return ceremonyKey(auth.SessionIDOf(sessionTokenOf(request)), purpose)
}

func logout(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	token := sessionTokenOf(request)
	if err := handler.service.Logout(request.Context(), token); err != nil {
		return nil, 0, err
	}
	handler.ceremonies.dropPrefix(ceremonyPrefix(auth.SessionIDOf(token)))
	handler.clearSessionCookie(writer)
	return stateBody{State: stateLoggedOut}, http.StatusOK, nil
}

func logoutAll(handler *Handler, writer http.ResponseWriter, request *http.Request) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	if err := handler.service.LogoutAll(request.Context()); err != nil {
		return nil, 0, err
	}
	handler.ceremonies.dropPrefix("")
	handler.clearSessionCookie(writer)
	return stateBody{State: stateLoggedOut}, http.StatusOK, nil
}

func changePassword(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	var body changePasswordBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if len(body.CurrentPassword) > auth.MaxPasswordBytes {
		return nil, 0, app.ErrInvalidCredentials
	}
	if len(body.NewPassword) > auth.MaxPasswordBytes {
		return nil, 0, auth.ErrPasswordTooLong
	}
	err := handler.service.ChangePassword(request.Context(), sessionTokenOf(request), body.CurrentPassword, body.NewPassword)
	return struct{}{}, http.StatusOK, err
}

func listSessions(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	views, err := handler.service.ListSessions(request.Context(), sessionTokenOf(request))
	if err != nil {
		return nil, 0, err
	}
	items := make([]sessionItem, 0, len(views))
	for _, view := range views {
		items = append(items, sessionItem{Current: view.Current, CreatedAt: view.CreatedAt, LastSeenAt: view.LastSeenAt, Address: view.Address})
	}
	return items, http.StatusOK, nil
}

func beginRegistration(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	var body registrationBeginBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if len(body.Password) > auth.MaxPasswordBytes {
		return nil, 0, app.ErrInvalidCredentials
	}
	ceremony, err := handler.service.BeginWebAuthnRegistration(request.Context(), sessionTokenOf(request), body.Password, body.Kind)
	if err != nil {
		return nil, 0, err
	}
	handler.ceremonies.put(handler.sessionCeremonyKey(request, purposeRegistration), heldState{purpose: purposeRegistration, state: ceremony.State})
	return ceremonyBody{WebAuthn: ceremony.Options}, http.StatusOK, nil
}

// finishRegistration consomme l'état de la session avant de lire le corps : un essai refusé ne se rejoue pas.
func finishRegistration(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	held, found := handler.ceremonies.take(handler.sessionCeremonyKey(request, purposeRegistration), purposeRegistration)
	if !found {
		return nil, 0, stepExpired()
	}
	var body registrationFinishBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if tooManyRunes(body.Name, maxCredentialNameRunes) {
		return nil, 0, app.ErrInvalidCredentialName
	}
	assertion, err := requireCredential(body.Credential)
	if err != nil {
		return nil, 0, err
	}
	err = handler.service.FinishWebAuthnRegistration(request.Context(), sessionTokenOf(request), held.state, body.Name, assertion)
	return struct{}{}, http.StatusCreated, err
}

func listCredentials(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	views, err := handler.service.ListCredentials(request.Context())
	if err != nil {
		return nil, 0, err
	}
	items := make([]credentialItem, 0, len(views))
	for _, view := range views {
		items = append(items, credentialItem{ID: view.ID, Name: view.Name, Kind: view.Kind, CreatedAt: view.CreatedAt, LastUsedAt: view.LastUsedAt})
	}
	return items, http.StatusOK, nil
}

// credentialIDOf borne l'identifiant du chemin avant tout appel au Service.
func credentialIDOf(request *http.Request) (string, error) {
	id := request.PathValue(pathCredentialID)
	if len(id) > maxCredentialIDBytes {
		return "", fieldTooLong(pathCredentialID)
	}
	return id, nil
}

func renameCredential(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	id, err := credentialIDOf(request)
	if err != nil {
		return nil, 0, err
	}
	var body nameBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if tooManyRunes(body.Name, maxCredentialNameRunes) {
		return nil, 0, app.ErrInvalidCredentialName
	}
	return struct{}{}, http.StatusOK, handler.service.RenameCredential(request.Context(), id, body.Name)
}

func deleteCredential(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	id, err := credentialIDOf(request)
	if err != nil {
		return nil, 0, err
	}
	password, err := decodePassword(request)
	if err != nil {
		return nil, 0, err
	}
	return struct{}{}, http.StatusOK, handler.service.DeleteCredential(request.Context(), sessionTokenOf(request), id, password)
}

func beginTOTPSetup(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	password, err := decodePassword(request)
	if err != nil {
		return nil, 0, err
	}
	enrolment, err := handler.service.BeginTOTPSetup(request.Context(), sessionTokenOf(request), password)
	if err != nil {
		return nil, 0, err
	}
	handler.ceremonies.put(handler.sessionCeremonyKey(request, purposeTOTPSetup), heldState{purpose: purposeTOTPSetup, state: enrolment.State})
	body := totpEnrolmentBody{OtpauthURI: enrolment.OtpauthURI, Secret: enrolment.SecretBase32}
	if len(enrolment.QRPNG) > 0 {
		encoded := base64.StdEncoding.EncodeToString(enrolment.QRPNG)
		body.QRPNGBase64 = &encoded
	}
	return body, http.StatusOK, nil
}

func finishTOTPSetup(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	held, found := handler.ceremonies.take(handler.sessionCeremonyKey(request, purposeTOTPSetup), purposeTOTPSetup)
	if !found {
		return nil, 0, stepExpired()
	}
	var body codeBody
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if len(body.Code) > maxCodeBytes {
		return nil, 0, fieldTooLong("code")
	}
	return struct{}{}, http.StatusCreated, handler.service.FinishTOTPSetup(request.Context(), sessionTokenOf(request), held.state, body.Code)
}

func removeTOTP(handler *Handler, _ http.ResponseWriter, request *http.Request) (any, int, error) {
	password, err := decodePassword(request)
	if err != nil {
		return nil, 0, err
	}
	return struct{}{}, http.StatusOK, handler.service.RemoveTOTP(request.Context(), sessionTokenOf(request), password)
}
