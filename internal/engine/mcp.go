package engine

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mcpConfig struct {
	Servers map[string]mcpServerConfig `json:"servers"`
}

func (e *execution) sampleForMCP(ctx context.Context, provider Provider, model, thinking string, params *mcp.CreateMessageParams) (*mcp.CreateMessageResult, error) {
	messages := make([]Message, 0, len(params.Messages))
	for _, source := range params.Messages {
		message := Message{Role: string(source.Role)}
		switch content := source.Content.(type) {
		case *mcp.TextContent:
			message.Content = content.Text
		case *mcp.ImageContent:
			message.Images = []Image{{Data: base64.StdEncoding.EncodeToString(content.Data), MimeType: content.MIMEType}}
		default:
			data, _ := json.Marshal(content)
			message.Content = string(data)
		}
		messages = append(messages, message)
	}
	_ = e.emit("mcp.sampling_requested", map[string]any{"model": model, "maxTokens": params.MaxTokens, "messageCount": len(messages)})
	completion, err := e.complete(ctx, provider, CompletionRequest{SessionID: e.command.SessionID, Model: model, System: params.SystemPrompt, Messages: messages, ThinkingLevel: thinking, MaxTokens: int(params.MaxTokens)}, nil, nil)
	if err != nil {
		return nil, err
	}
	_ = e.emit("mcp.sampling_completed", map[string]any{"model": model, "usage": completion.Usage})
	return &mcp.CreateMessageResult{Role: mcp.Role("assistant"), Model: model, StopReason: "endTurn", Content: &mcp.TextContent{Text: completion.Text}}, nil
}

func (e *execution) waitForMCPElicitation(ctx context.Context, params *mcp.ElicitParams) (*mcp.ElicitResult, error) {
	if e.interactionPoller == nil {
		return nil, fmt.Errorf("MCP elicitation requires an interaction poller")
	}
	request := map[string]any{"mode": params.Mode, "message": params.Message, "requestedSchema": params.RequestedSchema, "url": params.URL, "elicitationId": params.ElicitationID}
	e.mu.Lock()
	e.elicitationSequence++
	sequence := e.elicitationSequence
	e.mu.Unlock()
	interactionID := stableElicitationID(e.command.RunID, sequence, request)
	if err := e.emit("input.requested", map[string]any{"interactionId": interactionID, "toolCallId": "mcp-elicitation-" + interactionID, "kind": "mcp_elicitation", "request": request}); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		reply, err := e.interactionPoller(ctx, interactionID)
		if err != nil {
			return nil, err
		}
		if reply.Ready {
			result := &mcp.ElicitResult{Action: "accept"}
			if reply.Cancelled {
				result.Action = "cancel"
			} else if wrapped, ok := reply.Response.(map[string]any); ok {
				if action, ok := wrapped["action"].(string); ok && (action == "accept" || action == "decline" || action == "cancel") {
					result.Action = action
					if content, ok := wrapped["content"].(map[string]any); ok {
						result.Content = content
					}
				} else {
					result.Content = wrapped
				}
			} else {
				return nil, fmt.Errorf("MCP elicitation response must be a JSON object")
			}
			_ = e.emit("mcp.elicitation_completed", map[string]any{"interactionId": interactionID, "action": result.Action})
			return result, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// A stable ID lets an expired ECS lease replay the provider/MCP exchange and
// consume the already persisted answer instead of creating a second prompt.
func stableElicitationID(runID string, sequence uint64, request map[string]any) string {
	encoded, _ := json.Marshal(stable(request))
	sum := sha256.Sum256(append([]byte(fmt.Sprintf("%s:%d:", runID, sequence)), encoded...))
	value := sum[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

type mcpServerConfig struct {
	Transport        string            `json:"transport"`
	Command          string            `json:"command"`
	URL              string            `json:"url"`
	CWD              string            `json:"cwd"`
	Args             []string          `json:"args"`
	Env              map[string]string `json:"env"`
	Headers          map[string]string `json:"headers"`
	KeepAliveSeconds int               `json:"keepAliveSeconds"`
	MaxReconnects    *int              `json:"maxReconnects"`
}
type mcpLoaded struct {
	tools    []ToolDefinition
	approval map[string]bool
	sessions []*mcp.ClientSession
}
type mcpHost struct {
	sample func(context.Context, *mcp.CreateMessageParams) (*mcp.CreateMessageResult, error)
	elicit func(context.Context, *mcp.ElicitParams) (*mcp.ElicitResult, error)
}

func (m *mcpLoaded) close() {
	for _, session := range m.sessions {
		_ = session.Close()
	}
}

func loadMCP(ctx context.Context, cwd string, host mcpHost) (*mcpLoaded, error) {
	loaded := &mcpLoaded{approval: map[string]bool{}}
	configPath := os.Getenv("AGENT_MCP_CONFIG")
	if configPath == "" {
		return loaded, nil
	}
	if !filepath.IsAbs(configPath) {
		configPath = filepath.Join(cwd, configPath)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var config mcpConfig
	if err = json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("decode MCP config: %w", err)
	}
	for serverName, server := range config.Servers {
		options := &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{RootsV2: &mcp.RootCapabilities{}}, KeepAlive: time.Duration(server.KeepAliveSeconds) * time.Second}
		if host.sample != nil {
			options.CreateMessageHandler = func(callCtx context.Context, request *mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error) {
				return host.sample(callCtx, request.Params)
			}
		}
		if host.elicit != nil {
			options.ElicitationHandler = func(callCtx context.Context, request *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
				return host.elicit(callCtx, request.Params)
			}
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "ergo-agent-go", Version: RuntimeVersion}, options)
		client.AddRoots(&mcp.Root{URI: "file://" + filepath.ToSlash(cwd), Name: filepath.Base(cwd)})
		transport, transportErr := mcpTransport(cwd, server)
		if transportErr != nil {
			loaded.close()
			return nil, fmt.Errorf("MCP %s: %w", serverName, transportErr)
		}
		session, connectErr := client.Connect(ctx, transport, nil)
		if connectErr != nil {
			loaded.close()
			return nil, fmt.Errorf("connect MCP %s: %w", serverName, connectErr)
		}
		loaded.sessions = append(loaded.sessions, session)
		prefix := "mcp__" + safeMCPName(serverName)
		caps := session.InitializeResult().Capabilities
		if caps != nil && caps.Tools != nil {
			for remoteTool, iterErr := range session.Tools(ctx, nil) {
				if iterErr != nil {
					loaded.close()
					return nil, fmt.Errorf("list MCP tools %s: %w", serverName, iterErr)
				}
				remoteTool := remoteTool
				name := prefix + "__" + safeMCPName(remoteTool.Name)
				if remoteTool.Annotations != nil && ((remoteTool.Annotations.DestructiveHint != nil && *remoteTool.Annotations.DestructiveHint) || (remoteTool.Annotations.OpenWorldHint != nil && *remoteTool.Annotations.OpenWorldHint)) {
					loaded.approval[name] = true
				}
				schema := map[string]any{"type": "object"}
				if raw, marshalErr := json.Marshal(remoteTool.InputSchema); marshalErr == nil {
					_ = json.Unmarshal(raw, &schema)
				}
				loaded.tools = append(loaded.tools, ToolDefinition{Name: name, Description: first(remoteTool.Description, "MCP tool "+serverName+"/"+remoteTool.Name), Parameters: schema, Execute: func(callCtx context.Context, args json.RawMessage) (ToolResult, error) {
					var input any
					if err := json.Unmarshal(args, &input); err != nil {
						return ToolResult{}, err
					}
					result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: remoteTool.Name, Arguments: input})
					if err != nil {
						return ToolResult{}, err
					}
					converted := mcpToolResult(result)
					if result.IsError {
						return converted, fmt.Errorf("MCP tool returned an error")
					}
					return converted, nil
				}})
			}
		}
		if caps != nil && caps.Resources != nil {
			loaded.tools = append(loaded.tools, mcpResourceTools(prefix, serverName, session)...)
		}
		if caps != nil && caps.Prompts != nil {
			loaded.tools = append(loaded.tools, mcpPromptTools(prefix, serverName, session)...)
		}
	}
	return loaded, nil
}

func mcpTransport(cwd string, config mcpServerConfig) (mcp.Transport, error) {
	switch config.Transport {
	case "stdio":
		command := exec.Command(expandEnv(config.Command), expandStrings(config.Args)...)
		command.Dir = cwd
		if config.CWD != "" {
			command.Dir = expandEnv(config.CWD)
			if !filepath.IsAbs(command.Dir) {
				command.Dir = filepath.Join(cwd, command.Dir)
			}
		}
		command.Env = restrictedEnv(os.Environ())
		for key, value := range config.Env {
			command.Env = append(command.Env, key+"="+expandEnv(value))
		}
		return &mcp.CommandTransport{Command: command}, nil
	case "http":
		if config.URL == "" {
			return nil, fmt.Errorf("HTTP URL is required")
		}
		httpClient := &http.Client{Transport: &headerRoundTripper{base: http.DefaultTransport, headers: expandMap(config.Headers)}}
		transport := &mcp.StreamableClientTransport{Endpoint: expandEnv(config.URL), HTTPClient: httpClient}
		if config.MaxReconnects != nil {
			transport.MaxRetries = *config.MaxReconnects
		}
		return transport, nil
	default:
		return nil, fmt.Errorf("invalid transport %q", config.Transport)
	}
}

func mcpResourceTools(prefix, server string, session *mcp.ClientSession) []ToolDefinition {
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	return []ToolDefinition{
		{Name: prefix + "__list_resources", Description: "List MCP resources and resource templates exposed by " + server, Parameters: object(map[string]any{}), Execute: func(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
			resources := []*mcp.Resource{}
			for item, err := range session.Resources(ctx, nil) {
				if err != nil {
					return ToolResult{}, err
				}
				resources = append(resources, item)
			}
			templates := []*mcp.ResourceTemplate{}
			for item, err := range session.ResourceTemplates(ctx, nil) {
				if err != nil {
					return ToolResult{}, err
				}
				templates = append(templates, item)
			}
			return jsonToolResult(map[string]any{"resources": resources, "resourceTemplates": templates})
		}},
		{Name: prefix + "__read_resource", Description: "Read an MCP resource from " + server + " by URI", Parameters: object(map[string]any{"uri": map[string]any{"type": "string"}}, "uri"), Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			var input struct {
				URI string `json:"uri"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return ToolResult{}, err
			}
			result, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: input.URI})
			if err != nil {
				return ToolResult{}, err
			}
			texts := []string{}
			for _, content := range result.Contents {
				if content.Text != "" {
					texts = append(texts, content.Text)
				} else {
					texts = append(texts, "[Binary resource: "+content.URI+" ("+content.MIMEType+")]")
				}
			}
			return ToolResult{Text: strings.Join(texts, "\n"), Details: map[string]any{"contents": result.Contents}}, nil
		}},
	}
}
func mcpPromptTools(prefix, server string, session *mcp.ClientSession) []ToolDefinition {
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
	}
	return []ToolDefinition{
		{Name: prefix + "__list_prompts", Description: "List MCP prompts exposed by " + server, Parameters: object(map[string]any{}), Execute: func(ctx context.Context, _ json.RawMessage) (ToolResult, error) {
			prompts := []*mcp.Prompt{}
			for item, err := range session.Prompts(ctx, nil) {
				if err != nil {
					return ToolResult{}, err
				}
				prompts = append(prompts, item)
			}
			return jsonToolResult(prompts)
		}},
		{Name: prefix + "__get_prompt", Description: "Render an MCP prompt from " + server, Parameters: object(map[string]any{"name": map[string]any{"type": "string"}, "arguments": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}}, "name"), Execute: func(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
			var input struct {
				Name      string            `json:"name"`
				Arguments map[string]string `json:"arguments"`
			}
			if err := json.Unmarshal(raw, &input); err != nil {
				return ToolResult{}, err
			}
			result, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: input.Name, Arguments: input.Arguments})
			if err != nil {
				return ToolResult{}, err
			}
			return jsonToolResult(result)
		}},
	}
}

func mcpToolResult(result *mcp.CallToolResult) ToolResult {
	converted := ToolResult{Details: map[string]any{"structuredContent": result.StructuredContent, "isError": result.IsError}}
	texts := []string{}
	for _, content := range result.Content {
		switch value := content.(type) {
		case *mcp.TextContent:
			texts = append(texts, value.Text)
		case *mcp.ImageContent:
			converted.Images = append(converted.Images, Image{Data: base64.StdEncoding.EncodeToString(value.Data), MimeType: value.MIMEType})
		case *mcp.EmbeddedResource:
			if value.Resource.Text != "" {
				texts = append(texts, value.Resource.Text)
			} else {
				texts = append(texts, "[Binary resource: "+value.Resource.URI+"]")
			}
		default:
			data, _ := json.Marshal(value)
			texts = append(texts, string(data))
		}
	}
	converted.Text = strings.Join(texts, "\n")
	return converted
}
func jsonToolResult(value any) (ToolResult, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Text: string(data)}, nil
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, value := range h.headers {
		clone.Header.Set(key, value)
	}
	return h.base.RoundTrip(clone)
}
func safeMCPName(value string) string {
	return regexp.MustCompile(`[^a-zA-Z0-9_-]`).ReplaceAllString(value, "_")
}
func expandEnv(value string) string {
	return os.Expand(value, func(name string) string { return os.Getenv(name) })
}
func expandStrings(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = expandEnv(value)
	}
	return out
}
func expandMap(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = expandEnv(value)
	}
	return out
}
