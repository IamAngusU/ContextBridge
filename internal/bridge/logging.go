package bridge

import (
	"strconv"
	"strings"
)

const maximumLoggedValueBytes = 2048

// quotedLogValue preserves readable text while escaping line breaks, control
// characters, quotes, and backslashes. Values originating in jobs, adapters,
// schedules, or filesystem errors must cross this boundary before entering a
// line-oriented service log.
func quotedLogValue(value string) string {
	if len(value) > maximumLoggedValueBytes {
		value = truncateUTF8(value, maximumLoggedValueBytes) + " [truncated]"
	}
	// Keep these explicit replacements even though QuoteToGraphic also escapes
	// them: they are the auditable CWE-117 boundary and are recognized by the
	// independent CodeQL data-flow model.
	value = strings.ReplaceAll(value, "\r", `\r`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strconv.QuoteToGraphic(value)
}

func quotedLogError(err error) string {
	if err == nil {
		return `""`
	}
	return quotedLogValue(err.Error())
}
