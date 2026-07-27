// Package message defines the SDK message and content contracts.
package message

import "encoding/json"

type Image struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

type Message struct {
	Role              string         `json:"role"`
	API               string         `json:"api,omitempty"`
	Provider          string         `json:"provider,omitempty"`
	Model             string         `json:"model,omitempty"`
	Timestamp         int64          `json:"timestamp,omitempty"`
	Content           string         `json:"content,omitempty"`
	TextSignature     string         `json:"text_signature,omitempty"`
	Thinking          string         `json:"thinking,omitempty"`
	ThinkingSignature string         `json:"thinking_signature,omitempty"`
	ToolCallID        string         `json:"tool_call_id,omitempty"`
	ToolName          string         `json:"tool_name,omitempty"`
	ToolCalls         []ToolCall     `json:"tool_calls,omitempty"`
	Images            []Image        `json:"images,omitempty"`
	Usage             map[string]any `json:"usage,omitempty"`
	StopReason        string         `json:"stop_reason,omitempty"`
	ErrorMessage      string         `json:"error_message,omitempty"`
	IsError           bool           `json:"is_error,omitempty"`
	AddedToolNames    []string       `json:"added_tool_names,omitempty"`
}

type ToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}
