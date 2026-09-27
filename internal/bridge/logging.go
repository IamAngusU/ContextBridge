package bridge

import "strconv"

const maximumLoggedValueBytes = 2048

// quotedLogValue preserves readable text while escaping line breaks, control
// characters, quotes, and backslashes. Values originating in jobs, adapters,
// schedules, or filesystem errors must cross this boundary before entering a
// line-oriented service log.
func quotedLogValue(value string) string {
	if len(value) > maximumLoggedValueBytes {
		value = truncateUTF8(value, maximumLoggedValueBytes) + " [truncated]"
	}
	return strconv.QuoteToGraphic(value)
}

func quotedLogError(err error) string {
	if err == nil {
		return `""`
	}
	return quotedLogValue(err.Error())
}
