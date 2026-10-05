package resourceactivity

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IamAngusU/ContextBridge/internal/strictjson"
)

const ResourceActivityV1 = "contextbridge.resource-activity.v1"
const MaximumActivityItems = 32
const MaximumActivityBytes = 24 << 10

// ResourceActivity is opt-in, adapter-reported evidence, not a model's narration
// or an independent attestation. It shares the final result's ACL and retention.
type ResourceActivity struct {
	Schema    string        `json:"schema"`
	Items     []ResourceUse `json:"items"`
	Truncated bool          `json:"truncated,omitempty"`
}

type ResourceUse struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Action string `json:"action"`
	Label  string `json:"label"`
	Ref    string `json:"ref,omitempty"`
	URL    string `json:"url,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

var activityID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,95}$`)
var activitySHA256 = regexp.MustCompile(`^[a-f0-9]{64}$`)

// DecodeResourceActivity fails closed as a whole: a partially accepted manifest
// would make counts misleading. Never fetch URLs, resolve refs, or inspect text.
func DecodeResourceActivity(raw []byte) (*ResourceActivity, error) {
	var activity ResourceActivity
	if len(raw) > MaximumActivityBytes || strictjson.Decode(raw, &activity) != nil ||
		activity.Schema != ResourceActivityV1 || activity.Items == nil || len(activity.Items) > MaximumActivityItems {
		return nil, errors.New("invalid resource activity manifest")
	}
	seen := make(map[string]bool)
	for _, item := range activity.Items {
		allowed := false
		switch item.Kind {
		case "file":
			allowed = item.Action == "read" || item.Action == "inspected" || item.Action == "created" || item.Action == "updated"
		case "image":
			allowed = item.Action == "inspected"
		case "web":
			allowed = item.Action == "read" || item.Action == "cited"
		case "artifact":
			allowed = item.Action == "created" || item.Action == "reused"
		case "tool":
			allowed = item.Action == "executed"
		}
		if !allowed || !activityID.MatchString(item.ID) || seen[item.ID] ||
			!safeActivityLabel(item.Label) || (item.Ref != "" && !activityID.MatchString(item.Ref)) ||
			(item.SHA256 != "" && !activitySHA256.MatchString(item.SHA256)) {
			return nil, errors.New("invalid resource activity item")
		}
		seen[item.ID] = true
		// File/image labels are basenames, not host paths. Refs are opaque tokens,
		// not paths or download capabilities. Web URLs cannot contain credentials,
		// queries or fragments; consumers must not auto-fetch them (including icons).
		if item.Kind != "web" {
			if item.URL != "" || strings.ContainsAny(item.Label, `/\:`) {
				return nil, errors.New("resource activity must not contain host paths")
			}
		} else if item.URL != "" {
			u, err := url.Parse(item.URL)
			if err != nil || len(item.URL) > 2048 || !safeActivityText(item.URL) || u.Opaque != "" ||
				(u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil ||
				u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(item.URL, `\#`) {
				return nil, errors.New("invalid resource activity URL")
			}
		}
	}
	// JSON escaping can make otherwise short input much larger on the wire.
	canonical, err := json.Marshal(activity)
	if err != nil || len(canonical) > MaximumActivityBytes {
		return nil, errors.New("resource activity exceeds canonical byte limit")
	}
	return &activity, nil
}

func safeActivityLabel(label string) bool {
	return label != "" && len(label) <= 256 && strings.TrimSpace(label) == label && safeActivityText(label)
}

func safeActivityText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}
