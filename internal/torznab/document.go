package torznab

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"
	"time"
)

const (
	// maxXMLBytes plafonne une réponse XML : lire limite + 1 octet donne ErrBadResponse, jamais une troncature.
	maxXMLBytes = 4 << 20
	// maxItems plafonne les éléments <item> d'une recherche ; au-delà, la réponse est hostile.
	maxItems = 500
	// maxDepth plafonne l'imbrication ; un document Torznab légitime ne dépasse pas cinq niveaux.
	maxDepth = 16

	errorElementName = "error"
)

// visitor reçoit les événements d'un document déjà validé ; path est la pile des noms locaux, élément courant compris.
type visitor interface {
	start(element xml.StartElement, path []string) error
	text(data []byte, path []string)
	end(path []string) error
}

// readCapped lit au plus maxXMLBytes octets avant toute analyse.
func readCapped(body io.Reader) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(body, maxXMLBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: body read failed", ErrUnavailable)
	}
	if len(content) > maxXMLBytes {
		return nil, fmt.Errorf("%w: body larger than %d bytes", ErrBadResponse, maxXMLBytes)
	}
	return content, nil
}

// isXMLContentType accepte application/xml, text/xml et tout type « +xml » (application/rss+xml).
func isXMLContentType(header string) bool {
	mediaType, _, err := mime.ParseMediaType(header)
	if err != nil {
		return false
	}
	return mediaType == "application/xml" || mediaType == "text/xml" || strings.HasSuffix(mediaType, "+xml")
}

// newStrictDecoder ne déclare ni entité ni lecteur de jeu de caractères : seules les cinq entités prédéfinies et
// l'UTF-8 sont admis, rien n'est jamais chargé.
func newStrictDecoder(content []byte) *xml.Decoder {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	decoder.Strict = true
	return decoder
}

// decodeDocument parcourt le document jeton par jeton : toute directive (DOCTYPE, ENTITY) est refusée, la profondeur
// est bornée, la racine doit être root ou un élément <error>, et un document qui s'arrête avant la fin de sa racine
// est une erreur.
func decodeDocument(content []byte, root string, retryAfter time.Duration, handler visitor) error {
	decoder := newStrictDecoder(content)
	var path []string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%w: incomplete document", ErrBadResponse)
		}
		if err != nil {
			return fmt.Errorf("%w: malformed XML", ErrBadResponse)
		}
		switch typed := token.(type) {
		case xml.Directive:
			return fmt.Errorf("%w: XML directive refused", ErrHostileResponse)
		case xml.StartElement:
			path = append(path, typed.Name.Local)
			if err := checkStart(typed, path, root, retryAfter); err != nil {
				return err
			}
			if err := handler.start(typed, path); err != nil {
				return err
			}
		case xml.CharData:
			handler.text(typed, path)
		case xml.EndElement:
			if err := handler.end(path); err != nil {
				return err
			}
			path = path[:len(path)-1]
			if len(path) == 0 {
				return nil
			}
		}
	}
}

func checkStart(element xml.StartElement, path []string, root string, retryAfter time.Duration) error {
	if len(path) > maxDepth {
		return fmt.Errorf("%w: nesting deeper than %d", ErrHostileResponse, maxDepth)
	}
	if len(path) != 1 {
		return nil
	}
	if element.Name.Local == errorElementName {
		return elementError(attribute(element, "code"), retryAfter)
	}
	if element.Name.Local != root {
		return fmt.Errorf("%w: unexpected root element", ErrBadResponse)
	}
	return nil
}

// errorElementCode dit si la racine du corps d'une réponse HTTP en erreur est un élément <error>, et rend son code.
func errorElementCode(content []byte) (string, bool) {
	decoder := newStrictDecoder(content)
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", false
		}
		switch typed := token.(type) {
		case xml.Directive:
			return "", false
		case xml.StartElement:
			return attribute(typed, "code"), typed.Name.Local == errorElementName
		}
	}
}

func attribute(element xml.StartElement, name string) string {
	value, _ := attributeValue(element, name)
	return value
}

func attributeValue(element xml.StartElement, name string) (string, bool) {
	for _, candidate := range element.Attr {
		if candidate.Name.Local == name {
			return candidate.Value, true
		}
	}
	return "", false
}
