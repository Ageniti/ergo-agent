// Package tool defines the SDK tool contracts.
package tool

import (
	"context"
	"encoding/json"

	"github.com/ageniti/ergo-agent/message"
)

type ToolCall = message.ToolCall

type ToolDefinition struct {
	Name               string                                                                             `json:"name"`
	Description        string                                                                             `json:"description"`
	PromptSnippet      string                                                                             `json:"-"`
	PromptGuidelines   []string                                                                           `json:"-"`
	Parameters         map[string]any                                                                     `json:"parameters"`
	Execute            func(context.Context, json.RawMessage) (ToolResult, error)                         `json:"-"`
	ExecuteWithUpdates func(context.Context, json.RawMessage, func(ToolResult) error) (ToolResult, error) `json:"-"`
	ExecutionMode      string                                                                             `json:"-"`
}

type ToolResult struct {
	Text           string          `json:"text"`
	Images         []message.Image `json:"images,omitempty"`
	Details        map[string]any  `json:"details,omitempty"`
	Usage          map[string]any  `json:"usage,omitempty"`
	AddedToolNames []string        `json:"addedToolNames,omitempty"`
	Terminate      bool            `json:"terminate,omitempty"`
}

type (
	Call       = ToolCall
	Definition = ToolDefinition
	Result     = ToolResult
)
