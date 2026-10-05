package resourceactivity

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestResourceActivityStrictValidation(t *testing.T) {
	valid := ResourceActivity{Schema: ResourceActivityV1, Items: []ResourceUse{
		{ID: "a1", Kind: "file", Action: "read", Label: "README.md", Ref: "res_one"},
		{ID: "a2", Kind: "web", Action: "cited", Label: "Documentation", URL: "https://example.test/docs"},
		{ID: "a3", Kind: "image", Action: "inspected", Label: "image.png"},
	}}
	raw, _ := json.Marshal(valid)
	if _, err := DecodeResourceActivity(raw); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ResourceActivity){
		func(a *ResourceActivity) { a.Schema = "wrong" },
		func(a *ResourceActivity) { a.Items = nil },
		func(a *ResourceActivity) { a.Items[1].ID = "a1" },
		func(a *ResourceActivity) { a.Items[0].Kind = "subagent" },
		func(a *ResourceActivity) { a.Items[0].Action = "thought_about" },
		func(a *ResourceActivity) { a.Items[0].Label = "C:\\private\\secret.md" },
		func(a *ResourceActivity) { a.Items[0].Label = "../../secret" },
		func(a *ResourceActivity) { a.Items[0].Label = "bad\x1b[0m" },
		func(a *ResourceActivity) { a.Items[0].Label = "bad\u202espoof" },
		func(a *ResourceActivity) { a.Items[0].Label = strings.Repeat("a", 257) },
		func(a *ResourceActivity) { a.Items[0].Ref = "file:///secret" },
		func(a *ResourceActivity) { a.Items[0].SHA256 = "wrong" },
		func(a *ResourceActivity) { a.Items[0].URL = "https://example.test/secret" },
		func(a *ResourceActivity) { a.Items[1].URL = "https://user:password@example.test" },
		func(a *ResourceActivity) { a.Items[1].URL = "https://example.test/?token=secret" },
		func(a *ResourceActivity) { a.Items[1].URL = "https://example.test/#secret" },
		func(a *ResourceActivity) { a.Items[1].URL = "javascript:alert(1)" },
		func(a *ResourceActivity) { a.Items[1].URL = "file:///secret" },
		func(a *ResourceActivity) { a.Items = append(a.Items, make([]ResourceUse, 33)...) },
	} {
		var candidate ResourceActivity
		_ = json.Unmarshal(raw, &candidate)
		mutate(&candidate)
		bad, _ := json.Marshal(candidate)
		if _, err := DecodeResourceActivity(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	for _, bad := range []string{
		`{"schema":"contextbridge.resource-activity.v1","items":[],"items":[]}`,
		`{"schema":"contextbridge.resource-activity.v1","items":[],"secret":"no"}`,
		`{"schema":"contextbridge.resource-activity.v1","items":[],"truncated":false,"Truncated":true}`,
		string(raw) + `{}`, strings.Repeat(" ", MaximumActivityBytes) + string(raw),
	} {
		if _, err := DecodeResourceActivity([]byte(bad)); err == nil {
			t.Fatal("accepted malformed activity")
		}
	}
	large := ResourceActivity{Schema: ResourceActivityV1}
	for index := 0; index < 32; index++ {
		large.Items = append(large.Items, ResourceUse{ID: fmt.Sprintf("a%d", index), Kind: "file", Action: "read", Label: strings.Repeat("<", 256)})
	}
	escaped, _ := json.Marshal(large)
	unescaped := []byte(strings.ReplaceAll(string(escaped), `\u003c`, "<"))
	if len(unescaped) >= MaximumActivityBytes {
		t.Fatal("invalid size fixture")
	}
	if _, err := DecodeResourceActivity(unescaped); err == nil {
		t.Fatal("escaping amplification accepted")
	}
}
