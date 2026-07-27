package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
)

func streamJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body any, onData func([]byte) error) (int, map[string]string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		headers := responseHeaderMap(response.Header)
		return response.StatusCode, headers, &ProviderHTTPError{StatusCode: response.StatusCode, Status: response.Status, Body: strings.TrimSpace(string(data)), RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")), Headers: headers}
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64*1024), 32<<20)
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = nil
		if data == "[DONE]" {
			return nil
		}
		return onData([]byte(data))
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return response.StatusCode, responseHeaderMap(response.Header), err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return response.StatusCode, responseHeaderMap(response.Header), err
	}
	return response.StatusCode, responseHeaderMap(response.Header), flush()
}

type toolCallAccumulator struct {
	ID, Name  string
	Arguments strings.Builder
	Metadata  map[string]any
}

func toolCallsFromAccumulators(values map[int]*toolCallAccumulator) []ToolCall {
	result := make([]ToolCall, 0, len(values))
	indices := make([]int, 0, len(values))
	for index := range values {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		value := values[index]
		if value == nil {
			continue
		}
		arguments := json.RawMessage(value.Arguments.String())
		if !json.Valid(arguments) {
			arguments = json.RawMessage(`{}`)
		}
		result = append(result, ToolCall{ID: first(value.ID, newID()), Name: value.Name, Arguments: arguments, Metadata: value.Metadata})
	}
	return result
}
