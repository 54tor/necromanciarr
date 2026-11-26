package web

import (
	"context"
	"encoding/json"
	"net/http"

	"necromanciarr/internal/i18n"
)

// Paramètres de chemin des routes de configuration ; PathValue rend itemId décodé (« %2F » compris).
const (
	pathConfigKey    = "key"
	pathConfigList   = "list"
	pathConfigItemID = "itemId"
)

// Les routes de configuration répondent par la ConfigView relue : l'interface affiche l'état et la relance requise.
func getConfig(handler *Handler, request *http.Request) (any, int, error) {
	if err := requireKnownQuery(request.URL.Query()); err != nil {
		return nil, 0, err
	}
	return handler.configView(request.Context())
}

func (handler *Handler) configView(ctx context.Context) (any, int, error) {
	view, err := handler.service.ConfigView(ctx)
	return view, http.StatusOK, err
}

// requiredBodyField refuse un corps sans le champ obligatoire (absent ou null).
func requiredBodyField(name string) error {
	return newAPIError(http.StatusBadRequest, errorCodeUsage, named(i18n.KeyErrorWebFieldRequired, name))
}

func putSetting(handler *Handler, request *http.Request) (any, int, error) {
	var body struct {
		Value json.RawMessage `json:"value"`
	}
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	isValueMissing := body.Value == nil || string(body.Value) == "null"
	if isValueMissing {
		return nil, 0, requiredBodyField("value")
	}
	if err := handler.service.SetSetting(request.Context(), request.PathValue(pathConfigKey), body.Value); err != nil {
		return nil, 0, err
	}
	return handler.configView(request.Context())
}

func deleteSetting(handler *Handler, request *http.Request) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	if err := handler.service.UnsetSetting(request.Context(), request.PathValue(pathConfigKey)); err != nil {
		return nil, 0, err
	}
	return handler.configView(request.Context())
}

func putItem(handler *Handler, request *http.Request) (any, int, error) {
	var body struct {
		Element map[string]any `json:"element"`
	}
	if err := decodeBody(request, &body); err != nil {
		return nil, 0, err
	}
	if body.Element == nil {
		return nil, 0, requiredBodyField("element")
	}
	list, itemID := request.PathValue(pathConfigList), request.PathValue(pathConfigItemID)
	if err := handler.service.PutItem(request.Context(), list, itemID, body.Element); err != nil {
		return nil, 0, err
	}
	return handler.configView(request.Context())
}

// itemChange est DeleteItem ou UnsetItem.
type itemChange func(ctx context.Context, list, itemID string) error

func (handler *Handler) changeItem(request *http.Request, change itemChange) (any, int, error) {
	if err := decodeBody(request, &struct{}{}); err != nil {
		return nil, 0, err
	}
	if err := change(request.Context(), request.PathValue(pathConfigList), request.PathValue(pathConfigItemID)); err != nil {
		return nil, 0, err
	}
	return handler.configView(request.Context())
}

func deleteItem(handler *Handler, request *http.Request) (any, int, error) {
	return handler.changeItem(request, handler.service.DeleteItem)
}

func unsetItem(handler *Handler, request *http.Request) (any, int, error) {
	return handler.changeItem(request, handler.service.UnsetItem)
}
