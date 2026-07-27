package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func doJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body, target any) (int, map[string]string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return resp.StatusCode, responseHeaderMap(resp.Header), err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		headers := responseHeaderMap(resp.Header)
		return resp.StatusCode, headers, &ProviderHTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(data)), RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")), Headers: headers}
	}
	if err := json.Unmarshal(data, target); err != nil {
		return resp.StatusCode, responseHeaderMap(resp.Header), fmt.Errorf("decode provider response: %w", err)
	}
	return resp.StatusCode, responseHeaderMap(resp.Header), nil
}

func responseHeaderMap(headers http.Header) map[string]string {
	result := make(map[string]string, len(headers))
	for key, values := range headers {
		result[key] = strings.Join(values, ", ")
	}
	return result
}

func mergeHeaders(base, override map[string]string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}
	for key, value := range override {
		if value == "" {
			delete(base, key)
			continue
		}
		base[key] = value
	}
	return base
}

func canReplayProviderState(provider string, message Message, request CompletionRequest) bool {
	if message.Model != "" && message.Model != request.Model {
		return false
	}
	return provider == "" || message.Provider == "" || message.Provider == provider
}

func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

func contentText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, raw := range v {
			if item, ok := raw.(map[string]any); ok {
				if text, ok := item["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func contentThinking(value any) string {
	items, ok := value.([]any)
	if !ok {
		return ""
	}
	parts := []string{}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "thinking" {
			continue
		}
		if text, ok := item["text"].(string); ok {
			parts = append(parts, text)
		}
		if thinking, ok := item["thinking"].([]any); ok {
			for _, rawPart := range thinking {
				if part, ok := rawPart.(map[string]any); ok {
					if text, ok := part["text"].(string); ok {
						parts = append(parts, text)
					}
				}
			}
		}
	}
	return strings.Join(parts, "")
}
