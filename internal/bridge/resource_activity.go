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
	activity, err := resourceactivity.DecodeResourceActivity(raw)
	if err != nil {
		return nil, "invalid"
	}
	canonical, _ := json.Marshal(activity)
	return canonical, "reported"
}
