package bocha

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestToolSearchesAndNormalizesResults(t *testing.T) {
	var authorization string
	var request searchRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("method=%s content-type=%s", r.Method, r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code":200,
			"log_id":"request-id",
			"msg":null,
			"data":{
				"_type":"SearchResponse",
				"queryContext":{"originalQuery":"Go Agent 新闻"},
				"webPages":{
					"webSearchUrl":"https://bochaai.com/search?q=go",
					"totalEstimatedMatches":123,
					"value":[
						{"name":"Result","url":"https://example.com/a","siteName":"Example","siteIcon":"https://example.com/icon.png","snippet":"Snippet","summary":"Summary","dateLastCrawled":"2026-07-26T10:00:00Z"},
						{"name":"Missing URL","snippet":"ignored"},
						{"name":"Unsafe URL","url":"javascript:alert(1)","snippet":"ignored"}
					]
				},
				"images":{
					"value":[
						{"name":"Image","contentUrl":"https://example.com/image.jpg","hostPageUrl":"https://example.com/a","thumbnailUrl":"https://example.com/thumb.jpg","width":1200,"height":800},
						{"name":"Unsafe Image","contentUrl":"data:image/png;base64,abc"}
					]
				}
			}
		}`))
	}))
	defer server.Close()

	definition, err := Tool(Config{APIKey: "secret", BaseURL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if definition.ExecutionMode != "parallel" {
		t.Fatalf("execution mode=%q", definition.ExecutionMode)
	}
	result, err := definition.Execute(context.Background(), json.RawMessage(`{"query":" Go Agent 新闻 "}`))
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer secret" {
		t.Fatalf("authorization=%q", authorization)
	}
	if request.Query != "Go Agent 新闻" || request.Freshness != "noLimit" || request.Count != 10 || !request.Summary {
		t.Fatalf("request=%+v", request)
	}
	if !strings.Contains(result.Text, `"url": "https://example.com/a"`) || strings.Contains(result.Text, "Missing URL") || strings.Contains(result.Text, "Unsafe URL") {
		t.Fatalf("result=%s", result.Text)
	}
	if !strings.Contains(result.Text, `"siteIcon": "https://example.com/icon.png"`) || !strings.Contains(result.Text, `"datePublished": "2026-07-26T10:00:00Z"`) || strings.Contains(result.Text, "dateLastCrawled") {
		t.Fatalf("result=%s", result.Text)
	}
	if !strings.Contains(result.Text, `"contentUrl": "https://example.com/image.jpg"`) || strings.Contains(result.Text, "Unsafe Image") {
		t.Fatalf("result=%s", result.Text)
	}
	if result.Details["resultCount"] != 1 || result.Details["imageCount"] != 1 || result.Details["provider"] != "bocha" {
		t.Fatalf("details=%+v", result.Details)
	}
}

func TestToolValidatesInputAndDoesNotCallServer(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	definition, err := Tool(Config{APIKey: "secret", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"query":""}`,
		`{"query":"x","count":51}`,
		`{"query":"x","freshness":"yesterday"}`,
		`{"query":"x","freshness":"2026-07-27..2026-07-26"}`,
		`{"query":"x","unknown":true}`,
	} {
		if _, err := definition.Execute(context.Background(), json.RawMessage(input)); err == nil {
			t.Fatalf("input %s unexpectedly succeeded", input)
		}
	}
	if calls != 0 {
		t.Fatalf("server calls=%d", calls)
	}
}

func TestToolAcceptsOfficialMaximumCount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request searchRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Count != 50 {
			t.Fatalf("count=%d", request.Count)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"queryContext":{"originalQuery":"x"},"webPages":{"value":[]}}`))
	}))
	defer server.Close()
	definition, err := Tool(Config{APIKey: "secret", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = definition.Execute(context.Background(), json.RawMessage(`{"query":"x","count":50}`)); err != nil {
		t.Fatal(err)
	}
}

func TestToolReportsHTTPErrorWithoutLeakingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"bad request for top-secret"}`, http.StatusBadRequest)
	}))
	defer server.Close()
	definition, err := Tool(Config{APIKey: "top-secret", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = definition.Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("error=%v", err)
	}
}

func TestToolRejectsSuccessfulButInvalidResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"unexpected-shape"}`))
	}))
	defer server.Close()
	definition, err := Tool(Config{APIKey: "key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = definition.Execute(context.Background(), json.RawMessage(`{"query":"x"}`)); err == nil || !strings.Contains(err.Error(), "API error") {
		t.Fatalf("error=%v", err)
	}
}

func TestToolRejectsApplicationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"401","msg":"invalid API key","data":null}`))
	}))
	defer server.Close()
	definition, err := Tool(Config{APIKey: "key", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = definition.Execute(context.Background(), json.RawMessage(`{"query":"x"}`))
	if err == nil || !strings.Contains(err.Error(), `API error "401": invalid API key`) {
		t.Fatalf("error=%v", err)
	}
}

func TestNewFromEnvIsOptional(t *testing.T) {
	t.Setenv(environmentAPIKey, "")
	if _, enabled, err := NewFromEnv(); err != nil || enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	t.Setenv(environmentAPIKey, "key")
	t.Setenv(environmentBaseURL, "https://example.com/search")
	t.Setenv(environmentTimeout, "5s")
	extension, enabled, err := NewFromEnv()
	if err != nil || !enabled || extension.Name != extensionName || len(extension.Tools) != 1 {
		t.Fatalf("extension=%+v enabled=%v err=%v", extension, enabled, err)
	}
}
