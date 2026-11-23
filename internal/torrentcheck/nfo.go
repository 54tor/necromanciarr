package torrentcheck

import "bytes"

var utf8ByteOrderMark = []byte{0xEF, 0xBB, 0xBF}

type Variant struct {
	Label string
	Data  []byte
}

func NfoVariants(raw []byte) []Variant {
	withoutBOM := bytes.TrimPrefix(raw, utf8ByteOrderMark)
	lineFeedOnly := toLineFeed(withoutBOM)
	candidates := []Variant{
		{Label: "brut", Data: raw},
		{Label: "sans-bom", Data: withoutBOM},
		{Label: "lf", Data: lineFeedOnly},
		{Label: "crlf", Data: bytes.ReplaceAll(lineFeedOnly, []byte("\n"), []byte("\r\n"))},
	}
	return withoutDuplicateData(candidates)
}

func toLineFeed(data []byte) []byte {
	withoutCRLF := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(withoutCRLF, []byte("\r"), []byte("\n"))
}

func withoutDuplicateData(candidates []Variant) []Variant {
	var unique []Variant
	for _, candidate := range candidates {
		if !containsData(unique, candidate.Data) {
			unique = append(unique, candidate)
		}
	}
	return unique
}

func containsData(variants []Variant, data []byte) bool {
	for _, variant := range variants {
		if bytes.Equal(variant.Data, data) {
			return true
		}
	}
	return false
}
