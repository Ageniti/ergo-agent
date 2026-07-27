package mysqlrepo

import "testing"

func TestNormalizeEventPayloadConvertsTypedRuntimeCollections(t *testing.T) {
	type todo struct {
		ID   int    `json:"id"`
		Text string `json:"text"`
		Done bool   `json:"done"`
	}
	payload := map[string]any{
		"entries":        []map[string]any{{"id": "entry", "type": "message"}},
		"steps":          []map[string]any{{"id": "1", "text": "Do work"}},
		"completedSteps": []int{1, 2},
		"todos":          []todo{{ID: 1, Text: "Ship", Done: true}},
	}
	normalized, encoded, err := normalizeEventPayload(payload)
	if err != nil || len(encoded) == 0 {
		t.Fatalf("normalize err=%v encoded=%q", err, encoded)
	}
	for _, key := range []string{"entries", "steps", "completedSteps", "todos"} {
		items, ok := normalized[key].([]any)
		if !ok || len(items) == 0 {
			t.Fatalf("%s was not normalized: %#v", key, normalized[key])
		}
	}
	if normalized["todos"].([]any)[0].(map[string]any)["text"] != "Ship" {
		t.Fatalf("todos=%#v", normalized["todos"])
	}
}
