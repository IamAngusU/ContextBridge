package cluster

import (
	"strings"
	"unicode"
)

// NodeDiscriminator is display-only. The full ID remains the actual identity,
// so workers with equal names (or even equal short suffixes) stay distinct.
func NodeDiscriminator(id string) string {
	value := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, id)
	runes := []rune(value)
	if len(runes) > 6 {
		runes = runes[len(runes)-6:]
	}
	return string(runes)
}
