package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"necromanciarr/internal/app"
	"necromanciarr/internal/config"
	"necromanciarr/internal/i18n"
)

const (
	configTestItemID     = "http://example.com:7878"
	configTestItemPath   = "/api/config/lists/radarr/http%3A%2F%2Fexample.com%3A7878"
	configTestSecret     = "dummy-web-radarr-key-0000"
	configTestSettingKey = "linkCategory"
)

func maskedConfigView() app.ConfigView {
	return app.ConfigView{
		Settings: map[string]app.ConfigValue{
			"prowlarr":           {Origin: "override", Secret: true, Set: true},
			configTestSettingKey: {Value: "category-a", Origin: "override", Set: true},
		},
		Lists: map[string][]app.ConfigItem{
			"radarr": {{ItemID: configTestItemID, Element: map[string]any{"url": configTestItemID, "apiKey": nil}, Origin: "override"}},
		},
		Orphans: []config.OrphanOverride{},
	}
}

func TestConfigRoutes_getMasksSecrets(t *testing.T) {
	fake := &fakeService{configView: maskedConfigView()}
	handler := newTestHandler(t, fake, loopbackSettings())

	response := serve(handler, apiRequest(http.MethodGet, "/api/config", ""))

	var view app.ConfigView
	if err := json.Unmarshal([]byte(readBody(t, response)), &view); err != nil || response.Code != http.StatusOK {
		t.Fatalf("valeurObtenue %d (%v), valeurAttendue 200 et une ConfigView", response.Code, err)
	}
	prowlarr := view.Settings["prowlarr"]
	if prowlarr.Value != nil || !prowlarr.Secret || !prowlarr.Set {
		t.Fatalf("valeurObtenue %+v, valeurAttendue un secret posé de valeur null", prowlarr)
	}
	apiKey, isPresent := view.Lists["radarr"][0].Element["apiKey"]
	if !isPresent || apiKey != nil {
		t.Fatalf("valeurObtenue apiKey %v (présent %v), valeurAttendue un champ présent et null", apiKey, isPresent)
	}
}

func TestConfigRoutes_getRedactsAKnownSecretLeakedByTheView(t *testing.T) {
	leakyView := maskedConfigView()
	leakyView.Lists["radarr"][0].Element["url"] = configTestItemID + "?apikey=" + configTestSecret
	settings := loopbackSettings()
	settings.Radarr = []config.ArrService{{Kind: "radarr", BaseURL: configTestItemID, APIKey: configTestSecret}}
	handler := newTestHandler(t, &fakeService{configView: leakyView}, settings)

	body := readBody(t, serve(handler, apiRequest(http.MethodGet, "/api/config", "")))

	if strings.Contains(body, configTestSecret) {
		t.Fatalf("valeurObtenue %q, valeurAttendue le secret connu masqué", body)
	}
}

func TestConfigRoutes_putItemWithSecretAndNoKeyReturnsConflict(t *testing.T) {
	fake := &fakeService{errors: map[string]error{"PutItem": fmt.Errorf("radarr: %w", config.ErrSecretKeyMissing)}}
	handler := newTestHandler(t, fake, loopbackSettings())
	requestBody := `{"element": {"url": "` + configTestItemID + `", "apiKey": "` + configTestSecret + `"}}`
	valeurAttendue := i18n.New(i18n.English, nil).Text(i18n.KeyErrorSecretKeyMissing, nil)

	response := serve(handler, apiRequest(http.MethodPut, configTestItemPath, requestBody))

	body := readBody(t, response)
	expectError(t, "put item without secret key", body, response.Code, http.StatusConflict, app.ErrorCodeSecretKeyMissing)
	if valeurObtenue := decodeError(t, body).Error.Message; valeurObtenue != valeurAttendue {
		t.Fatalf("valeurObtenue %q, valeurAttendue %q", valeurObtenue, valeurAttendue)
	}
	if strings.Contains(body, configTestSecret) {
		t.Fatalf("valeurObtenue %q, valeurAttendue aucun secret dans l'erreur", body)
	}
}

func TestConfigRoutes_encodedItemIDReachesTheServiceDecoded(t *testing.T) {
	scenarios := []struct {
		method, path, body, call string
	}{
		{http.MethodPut, configTestItemPath, `{"element": {"url": "` + configTestItemID + `"}}`, "PutItem"},
		{http.MethodDelete, configTestItemPath, ``, "DeleteItem"},
		{http.MethodPost, configTestItemPath + "/unset", `{}`, "UnsetItem"},
	}
	for _, scenario := range scenarios {
		fake := &fakeService{configView: maskedConfigView()}
		handler := newTestHandler(t, fake, loopbackSettings())

		response := serve(handler, apiRequest(scenario.method, scenario.path, scenario.body))

		isCalled := fake.called(scenario.call) && fake.lastList == "radarr" && fake.lastItemID == configTestItemID
		if response.Code != http.StatusOK || !isCalled {
			t.Errorf("%s %s: valeurObtenue %d, list %q item %q, calls %v ; valeurAttendue 200 et l'id décodé %q",
				scenario.method, scenario.path, response.Code, fake.lastList, fake.lastItemID, fake.calls, configTestItemID)
		}
	}
}

func TestConfigRoutes_putSettingPassesTheValueAndAnswersTheView(t *testing.T) {
	fake := &fakeService{configView: maskedConfigView()}
	handler := newTestHandler(t, fake, loopbackSettings())
	valeurAttendue := `"category-b"`

	response := serve(handler, apiRequest(http.MethodPut, "/api/config/settings/"+configTestSettingKey, `{"value": `+valeurAttendue+`}`))

	var view app.ConfigView
	decodeErr := json.Unmarshal([]byte(readBody(t, response)), &view)
	if valeurObtenue := string(fake.lastSettingValue); response.Code != http.StatusOK || valeurObtenue != valeurAttendue || decodeErr != nil {
		t.Fatalf("valeurObtenue %d %q (%v), valeurAttendue 200 et %s", response.Code, valeurObtenue, decodeErr, valeurAttendue)
	}
	if !fake.called("SetSetting "+configTestSettingKey) || !fake.called("ConfigView") {
		t.Fatalf("valeurObtenue calls %v, valeurAttendue SetSetting puis ConfigView", fake.calls)
	}
}

func TestConfigRoutes_deleteSettingCallsUnset(t *testing.T) {
	fake := &fakeService{configView: maskedConfigView()}
	handler := newTestHandler(t, fake, loopbackSettings())

	response := serve(handler, apiRequest(http.MethodDelete, "/api/config/settings/"+configTestSettingKey, ""))

	if response.Code != http.StatusOK || !fake.called("UnsetSetting "+configTestSettingKey) {
		t.Fatalf("valeurObtenue %d, calls %v ; valeurAttendue 200 et UnsetSetting", response.Code, fake.calls)
	}
}

func TestConfigRoutes_refusesAMalformedBodyWithoutWriting(t *testing.T) {
	scenarios := []struct {
		name, path, body string
	}{
		{"setting without value", "/api/config/settings/" + configTestSettingKey, `{}`},
		{"setting with unknown field", "/api/config/settings/" + configTestSettingKey, `{"value": 1, "extra": 2}`},
		{"setting with trailing data", "/api/config/settings/" + configTestSettingKey, `{"value": 1} {}`},
		{"item without element", configTestItemPath, `{}`},
		{"item with a null element", configTestItemPath, `{"element": null}`},
		{"item with an array element", configTestItemPath, `{"element": ["a"]}`},
	}
	for _, scenario := range scenarios {
		fake := &fakeService{}
		handler := newTestHandler(t, fake, loopbackSettings())

		response := serve(handler, apiRequest(http.MethodPut, scenario.path, scenario.body))

		expectError(t, scenario.name, readBody(t, response), response.Code, http.StatusBadRequest, app.ErrorCodeUsage)
		if fake.called("SetSetting") || fake.called("PutItem") {
			t.Errorf("%s: valeurObtenue calls %v, valeurAttendue aucune écriture", scenario.name, fake.calls)
		}
	}
}

func TestConfigRoutes_mapsConfigErrorsToTranslatedStatuses(t *testing.T) {
	unknownKey := fmt.Errorf("%w: setting %q", app.ErrUnknownConfigKey, "notASetting")
	invalidStored := fmt.Errorf("%w: stored radarr item", config.ErrInvalidStoredConfig)
	englishTranslator := i18n.New(i18n.English, nil)
	scenarios := []struct {
		name, method, path, body, failingMethod string
		err                                     error
		expectedStatus                          int
		expectedCode                            string
		expectedKey                             i18n.Key
	}{
		{
			"unknown key", http.MethodPut, "/api/config/settings/notASetting", `{"value": 1}`, "SetSetting", unknownKey,
			http.StatusBadRequest, app.ErrorCodeUsage, i18n.KeyErrorUnknownConfigKey,
		},
		{
			"invalid stored configuration", http.MethodGet, "/api/config", "", "ConfigView", invalidStored,
			http.StatusUnprocessableEntity, app.ErrorCodeInvalidStoredConfig, i18n.KeyErrorInvalidStoredConfig,
		},
	}
	for _, scenario := range scenarios {
		fake := &fakeService{errors: map[string]error{scenario.failingMethod: scenario.err}}
		handler := newTestHandler(t, fake, loopbackSettings())

		response := serve(handler, apiRequest(scenario.method, scenario.path, scenario.body))

		body := readBody(t, response)
		expectError(t, scenario.name, body, response.Code, scenario.expectedStatus, scenario.expectedCode)
		if valeurObtenue, valeurAttendue := decodeError(t, body).Error.Message, englishTranslator.Text(scenario.expectedKey, nil); valeurObtenue != valeurAttendue {
			t.Errorf("%s: valeurObtenue %q, valeurAttendue %q", scenario.name, valeurObtenue, valeurAttendue)
		}
	}
}

func TestConfigRoutes_mutatingRoutesNeedTheTokenAndTheSameOrigin(t *testing.T) {
	fake := &fakeService{}
	withoutToken := serve(newTestHandler(t, fake, tokenSettings()), apiRequest(http.MethodPut, configTestItemPath, `{"element": {}}`))
	crossSite := apiRequest(http.MethodDelete, "/api/config/settings/"+configTestSettingKey, "")
	crossSite.Header.Del("X-Requested-With")
	refused := serve(newTestHandler(t, fake, loopbackSettings()), crossSite)
	formPost := apiRequest(http.MethodPost, configTestItemPath+"/unset", `{}`)
	formPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unsupported := serve(newTestHandler(t, fake, loopbackSettings()), formPost)

	isRefused := withoutToken.Code == http.StatusUnauthorized && refused.Code == http.StatusForbidden &&
		unsupported.Code == http.StatusUnsupportedMediaType
	if !isRefused || len(fake.calls) != 0 {
		t.Fatalf("valeurObtenue %d / %d / %d, calls %v ; valeurAttendue 401, 403, 415 sans appel",
			withoutToken.Code, refused.Code, unsupported.Code, fake.calls)
	}
}
