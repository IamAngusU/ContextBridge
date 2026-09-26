package terminalui

import (
	"fmt"
	"time"
)

// CompactDuration renders an elapsed wall-clock duration for live human
// surfaces. It deliberately does not imply progress, completion percentage,
// or remaining time. Negative values can occur after a wall-clock adjustment
// and are clamped to zero rather than underflowing into a misleading age.
func CompactDuration(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	seconds := int64(value / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%02dm%02ds", seconds/60, seconds%60)
	}
	if seconds < 86400 {
		return fmt.Sprintf("%02dh%02dm", seconds/3600, seconds%3600/60)
	}
	return fmt.Sprintf("%dd%02dh%02dm", seconds/86400, seconds%86400/3600, seconds%3600/60)
}

func compactDuration(value time.Duration) string {
	return CompactDuration(value)
}
