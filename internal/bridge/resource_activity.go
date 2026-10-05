package bridge

import (
	"encoding/json"

	"github.com/IamAngusU/ContextBridge/internal/resourceactivity"
)

func normalizeResourceActivity(raw json.RawMessage, spec OutputSpec, provider string) (json.RawMessage, string) {
	if !spec.Activity {
		return nil, ""
	}
	if provider != "adapter" || len(raw) == 0 {
		return nil, "not_reported"
	}
	return resourceactivity.Normalize(raw, true)
}

func normalizeProgressActivity(progress AdapterProgress, previous *AdapterProgress, enabled bool) AdapterProgress {
	if enabled && len(progress.Activity) == 0 && progress.ActivityStatus == "invalid" {
		return progress
	}
	if enabled && len(progress.Activity) == 0 && previous != nil {
		progress.Activity = append(json.RawMessage(nil), previous.Activity...)
		progress.ActivityStatus = previous.ActivityStatus
	} else {
		progress.Activity, progress.ActivityStatus = resourceactivity.Normalize(progress.Activity, enabled)
	}
	return progress
}

func cloneAdapterProgress(progress AdapterProgress) AdapterProgress {
	progress.Activity = append(json.RawMessage(nil), progress.Activity...)
	return progress
}
