package cluster

import "testing"

func TestScheduledActionOpenAPIContractIsScopedAndContentMinimized(t *testing.T) {
	document := relayOpenAPI()
	paths, ok := document["paths"].(map[string]interface{})
	if !ok {
		t.Fatal("OpenAPI paths are missing")
	}
	for _, path := range []string{
		"/v1/cluster/scheduled-actions/preview",
		"/v1/cluster/scheduled-actions",
		"/v1/cluster/scheduled-actions/{id}",
		"/v1/cluster/scheduled-actions/{id}/confirm",
	} {
		if _, exists := paths[path]; !exists {
			t.Fatalf("OpenAPI path %s is missing", path)
		}
	}
	components := document["components"].(map[string]interface{})
	schemas := components["schemas"].(map[string]interface{})
	action := schemas["ScheduledAction"].(map[string]interface{})
	properties := action["properties"].(map[string]interface{})
	for _, internal := range []string{"credential_hash", "adapter_principal"} {
		if _, disclosed := properties[internal]; disclosed {
			t.Fatalf("public scheduled action schema discloses %s", internal)
		}
	}
	limits := schemas["ScheduledActionLimits"].(map[string]interface{})
	limitProperties := limits["properties"].(map[string]interface{})
	if _, exists := limitProperties["schema"]; !exists {
		t.Fatal("scheduled action policy schema version is not documented")
	}
	producer := schemas["ProducerLimits"].(map[string]interface{})
	producerProperties := producer["properties"].(map[string]interface{})
	if _, exists := producerProperties["scheduled_actions"]; !exists {
		t.Fatal("producer scheduled-action authority is not documented")
	}
}
