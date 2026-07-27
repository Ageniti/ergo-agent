package engine

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

type toolset struct {
	cwd           string
	planMode      bool
	images        ImageGenerator
	todos         []todo
	nextTodo      int
	subagent      func(context.Context, string, string, string, string) (string, error)
	subagentNames []string
	subagentScope string
}
type todo struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

func (t *toolset) definitions(names []string) []ToolDefinition {
	all := map[string]ToolDefinition{}
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	all["read"] = ToolDefinition{Name: "read", Description: "Read the contents of a file. Supports text files and images (jpg, png, gif, webp, bmp). Images are sent as attachments. For text files, output is truncated to 2000 lines or 50KB (whichever is hit first). Use offset/limit for large files. When you need the full file, continue with offset until complete.", PromptSnippet: "Read file contents", PromptGuidelines: []string{"Use read to examine files instead of cat or sed."}, Parameters: object(map[string]any{"path": map[string]any{"type": "string", "description": "Path to the file to read (relative or absolute)"}, "offset": map[string]any{"type": "number", "description": "Line number to start reading from (1-indexed)"}, "limit": map[string]any{"type": "number", "description": "Maximum number of lines to read"}}, "path"), Execute: t.read, ExecutionMode: "parallel"}
	all["write"] = ToolDefinition{Name: "write", Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does. Automatically creates parent directories.", PromptSnippet: "Create or overwrite files", PromptGuidelines: []string{"Use write only for new files or complete rewrites."}, Parameters: object(map[string]any{"path": map[string]any{"type": "string", "description": "Path to the file to write (relative or absolute)"}, "content": map[string]any{"type": "string", "description": "Content to write to the file"}}, "path", "content"), Execute: t.write}
	replacement := map[string]any{"type": "object", "properties": map[string]any{"oldText": map[string]any{"type": "string", "description": "Exact text for one targeted replacement. It must be unique in the original file and must not overlap with any other edits[].oldText in the same call."}, "newText": map[string]any{"type": "string", "description": "Replacement text for this targeted edit."}}, "required": []string{"oldText", "newText"}}
	all["edit"] = ToolDefinition{Name: "edit", Description: "Edit a single file using exact text replacement. Every edits[].oldText must match a unique, non-overlapping region of the original file. If two changes affect the same block or nearby lines, merge them into one edit instead of emitting overlapping edits. Do not include large unchanged regions just to connect distant changes.", PromptSnippet: "Make precise file edits with exact text replacement, including multiple disjoint edits in one call", PromptGuidelines: []string{"Use edit for precise changes (edits[].oldText must match exactly)", "When changing multiple separate locations in one file, use one edit call with multiple entries in edits[] instead of multiple edit calls", "Each edits[].oldText is matched against the original file, not after earlier edits are applied. Do not emit overlapping or nested edits. Merge nearby changes into one edit.", "Keep edits[].oldText as small as possible while still being unique in the file. Do not pad with large unchanged regions."}, Parameters: object(map[string]any{"path": map[string]any{"type": "string", "description": "Path to the file to edit (relative or absolute)"}, "edits": map[string]any{"type": "array", "description": "One or more targeted replacements. Each edit is matched against the original file, not incrementally. Do not include overlapping or nested edits. If two changes touch the same block or nearby lines, merge them into one edit instead.", "items": replacement}}, "path", "edits"), Execute: t.edit}
	all["bash"] = ToolDefinition{Name: "bash", Description: "Execute a bash command in the current working directory. Returns stdout and stderr. Output is truncated to last 2000 lines or 50KB (whichever is hit first). If truncated, full output is saved to a temp file. Optionally provide a timeout in seconds.", PromptSnippet: "Execute bash commands (ls, grep, find, etc.)", Parameters: object(map[string]any{"command": map[string]any{"type": "string", "description": "Bash command to execute"}, "timeout": map[string]any{"type": "number", "description": "Timeout in seconds (optional, no default timeout)"}}, "command"), Execute: t.bash, ExecuteWithUpdates: t.bashWithUpdates, ExecutionMode: "sequential"}
	all["git_read"] = ToolDefinition{Name: "git_read", Description: "Inspect a Git repository without a shell or mutation-capable Git commands. Supports status, diff, log, and show.", PromptSnippet: "Read Git status, diffs, history, and revisions without modifying the repository", Parameters: object(map[string]any{
		"operation": map[string]any{"type": "string", "enum": []string{"status", "diff", "log", "show"}},
		"revision":  map[string]any{"type": "string", "description": "Revision for show (default: HEAD)"},
		"paths":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional path filters"},
		"staged":    map[string]any{"type": "boolean", "description": "For diff, inspect staged changes"},
		"maxCount":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "For log, maximum commits (default: 20)"},
	}, "operation"), Execute: t.gitRead, ExecutionMode: "parallel"}
	all["grep"] = ToolDefinition{Name: "grep", Description: "Search file contents for a pattern. Returns matching lines with file paths and line numbers. Respects .gitignore. Output is truncated to 100 matches or 50KB (whichever is hit first). Long lines are truncated to 500 chars.", PromptSnippet: "Search file contents for patterns (respects .gitignore)", Parameters: object(map[string]any{"pattern": map[string]any{"type": "string", "description": "Search pattern (regex or literal string)"}, "path": map[string]any{"type": "string", "description": "Directory or file to search (default: current directory)"}, "glob": map[string]any{"type": "string", "description": "Filter files by glob pattern, e.g. '*.ts' or '**/*.spec.ts'"}, "ignoreCase": map[string]any{"type": "boolean", "description": "Case-insensitive search (default: false)"}, "literal": map[string]any{"type": "boolean", "description": "Treat pattern as literal string instead of regex (default: false)"}, "context": map[string]any{"type": "number", "description": "Number of lines to show before and after each match (default: 0)"}, "limit": map[string]any{"type": "number", "description": "Maximum number of matches to return (default: 100)"}}, "pattern"), Execute: t.grep, ExecutionMode: "parallel"}
	all["find"] = ToolDefinition{Name: "find", Description: "Search for files by glob pattern. Returns matching file paths relative to the search directory. Respects .gitignore. Output is truncated to 1000 results or 50KB (whichever is hit first).", PromptSnippet: "Find files by glob pattern (respects .gitignore)", Parameters: object(map[string]any{"pattern": map[string]any{"type": "string", "description": "Glob pattern to match files, e.g. '*.ts', '**/*.json', or 'src/**/*.spec.ts'"}, "path": map[string]any{"type": "string", "description": "Directory to search in (default: current directory)"}, "limit": map[string]any{"type": "number", "description": "Maximum number of results (default: 1000)"}}, "pattern"), Execute: t.find, ExecutionMode: "parallel"}
	all["ls"] = ToolDefinition{Name: "ls", Description: "List directory contents. Returns entries sorted alphabetically, with '/' suffix for directories. Includes dotfiles. Output is truncated to 500 entries or 50KB (whichever is hit first).", PromptSnippet: "List directory contents", Parameters: object(map[string]any{"path": map[string]any{"type": "string", "description": "Directory to list (default: current directory)"}, "limit": map[string]any{"type": "number", "description": "Maximum number of entries to return (default: 500)"}}), Execute: t.ls, ExecutionMode: "parallel"}
	all["todo"] = ToolDefinition{Name: "todo", Description: "Manage a todo list. Actions: list, add (text), toggle (id), clear", Parameters: object(map[string]any{"action": map[string]any{"type": "string", "enum": []string{"list", "add", "toggle", "clear"}}, "text": map[string]any{"type": "string"}, "id": map[string]any{"type": "integer"}}, "action"), Execute: t.todo, ExecutionMode: "sequential"}
	if t.images != nil && !t.planMode {
		imageInput := map[string]any{"type": "object", "properties": map[string]any{
			"mimeType": map[string]any{"type": "string", "description": "MIME type such as image/png"},
			"data":     map[string]any{"type": "string", "description": "Base64-encoded image bytes"},
		}, "required": []string{"mimeType", "data"}}
		all["generate_image"] = ToolDefinition{
			Name:          "generate_image",
			Description:   "Generate an image, or create a variation from reference images. Returns generated image attachments and optional provider text. Use referencePaths for images already in the workspace, or images for base64 image input.",
			PromptSnippet: "Generate or edit images through the configured image provider",
			ExecutionMode: "sequential",
			Parameters: object(map[string]any{
				"prompt":         map[string]any{"type": "string", "description": "What to generate or how to transform the reference images"},
				"model":          map[string]any{"type": "string", "description": "Optional image model ID; defaults to the configured image model"},
				"images":         map[string]any{"type": "array", "description": "Optional base64 reference images", "items": imageInput},
				"referencePaths": map[string]any{"type": "array", "description": "Optional image file paths relative to the workspace", "items": map[string]any{"type": "string"}},
			}, "prompt"),
			Execute: t.generateImage,
		}
	}
	if len(t.subagentNames) > 0 {
		defaultScope := t.subagentScope
		if defaultScope == "" {
			defaultScope = "user"
		}
		agentSchema := map[string]any{"type": "string", "enum": t.subagentNames, "description": "Name of an available agent to invoke"}
		taskItem := map[string]any{"type": "object", "properties": map[string]any{"agent": agentSchema, "task": map[string]any{"type": "string", "description": "Task to delegate to the agent"}, "cwd": map[string]any{"type": "string", "description": "Working directory for the agent process"}}, "required": []string{"agent", "task"}}
		chainItem := map[string]any{"type": "object", "properties": map[string]any{"agent": agentSchema, "task": map[string]any{"type": "string", "description": "Task with optional {previous} placeholder for prior output"}, "cwd": map[string]any{"type": "string", "description": "Working directory for the agent process"}}, "required": []string{"agent", "task"}}
		subagentDescription := fmt.Sprintf("Delegate tasks to available specialized agents with isolated context. Available agents: %s. Modes: single (agent + task), parallel (tasks array), chain (sequential with {previous} placeholder). Agent scope defaults to the current Agent scope (%q). Override it only when intentionally switching between user and project Agents.", strings.Join(t.subagentNames, ", "), defaultScope)
		all["subagent"] = ToolDefinition{Name: "subagent", Description: subagentDescription, Parameters: object(map[string]any{
			"agent": agentSchema, "task": map[string]any{"type": "string", "description": "Task to delegate (for single mode)"}, "cwd": map[string]any{"type": "string", "description": "Working directory for the agent process (single mode)"},
			"tasks":      map[string]any{"type": "array", "description": "Array of {agent, task} for parallel execution", "items": taskItem},
			"chain":      map[string]any{"type": "array", "description": "Array of {agent, task} for sequential execution", "items": chainItem},
			"agentScope": map[string]any{"type": "string", "enum": []string{"user", "project", "both"}, "description": "Which Agent directories to use. Defaults to the current Agent scope.", "default": defaultScope}, "confirmProjectAgents": map[string]any{"type": "boolean", "description": "Prompt before switching into project-local Agents. Default: true.", "default": true},
		}), Execute: t.runSubagent}
	}
	optionSchema := map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}, "required": []string{"value", "label"}}
	questionSchema := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "label": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "options": map[string]any{"type": "array", "items": optionSchema}, "allowOther": map[string]any{"type": "boolean"}}, "required": []string{"id", "prompt", "options"}}
	all["questionnaire"] = ToolDefinition{Name: "questionnaire", Description: "Ask the user one or more questions. Use for clarifying requirements, getting preferences, or confirming decisions.", Parameters: object(map[string]any{"questions": map[string]any{"type": "array", "minItems": 1, "items": questionSchema}}, "questions"), Execute: t.questionnaire, ExecutionMode: "sequential"}
	seen := map[string]bool{}
	var out []ToolDefinition
	for _, name := range names {
		if tool, ok := all[name]; ok && !seen[name] {
			out = append(out, tool)
			seen[name] = true
		}
	}
	return out
}

func (t *toolset) generateImage(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	if t.images == nil {
		return ToolResult{}, fmt.Errorf("image generation is not configured")
	}
	var params struct {
		Prompt         string   `json:"prompt"`
		Model          string   `json:"model"`
		Images         []Image  `json:"images"`
		ReferencePaths []string `json:"referencePaths"`
	}
	if err := decode(raw, &params); err != nil {
		return ToolResult{}, err
	}
	if strings.TrimSpace(params.Prompt) == "" {
		return ToolResult{}, fmt.Errorf("prompt is required")
	}
	for index, image := range params.Images {
		if image.MimeType == "" || image.Data == "" {
			return ToolResult{}, fmt.Errorf("images[%d] requires mimeType and data", index)
		}
		if len(image.Data) > inlineImageMaxBase64 {
			return ToolResult{}, fmt.Errorf("images[%d] exceeds the %s inline image limit", index, formatBytes(inlineImageMaxBase64))
		}
	}
	for _, reference := range params.ReferencePaths {
		data, err := os.ReadFile(t.path(reference))
		if err != nil {
			return ToolResult{}, err
		}
		if len(data) > 20<<20 {
			return ToolResult{}, fmt.Errorf("reference image %q exceeds 20 MiB", reference)
		}
		mimeType := imageMIME(reference, data)
		if mimeType == "" {
			return ToolResult{}, fmt.Errorf("reference path %q is not a supported image", reference)
		}
		processed, err := processInlineImage(data, mimeType)
		if err != nil {
			return ToolResult{}, fmt.Errorf("process reference image %q: %w", reference, err)
		}
		params.Images = append(params.Images, Image{MimeType: processed.MIME, Data: processed.Data})
	}
	result := t.images.GenerateImage(ctx, ImageGenerationRequest{Model: params.Model, Prompt: params.Prompt, Images: params.Images})
	if result.StopReason != "stop" {
		return ToolResult{Text: first(result.ErrorMessage, "image generation did not complete"), Usage: result.Usage}, errors.New(first(result.ErrorMessage, "image generation did not complete"))
	}
	images := make([]Image, 0, len(result.Images))
	for index, image := range result.Images {
		decoded, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil {
			return ToolResult{}, fmt.Errorf("generated image %d has invalid base64 data", index+1)
		}
		processed, err := processInlineImage(decoded, image.MimeType)
		if err != nil {
			return ToolResult{}, fmt.Errorf("process generated image %d: %w", index+1, err)
		}
		images = append(images, Image{MimeType: processed.MIME, Data: processed.Data})
	}
	text := strings.TrimSpace(result.Text)
	if text == "" {
		text = fmt.Sprintf("Generated %d image(s) with %s.", len(images), result.Model)
	}
	return ToolResult{
		Text:   text,
		Images: images,
		Usage:  result.Usage,
		Details: map[string]any{
			"imageGeneration": map[string]any{
				"api":        result.API,
				"provider":   result.Provider,
				"model":      result.Model,
				"responseId": result.ResponseID,
			},
		},
	}, nil
}

func (t *toolset) questionnaire(_ context.Context, raw json.RawMessage) (ToolResult, error) {
	var params struct {
		Questions []struct {
			ID, Label, Prompt string
			Options           []struct{ Value, Label, Description string }
			AllowOther        *bool
		}
	}
	if err := decode(raw, &params); err != nil {
		return ToolResult{}, err
	}
	if len(params.Questions) == 0 {
		return ToolResult{}, fmt.Errorf("no questions provided")
	}
	seen := map[string]bool{}
	for i, question := range params.Questions {
		if strings.TrimSpace(question.ID) == "" || strings.TrimSpace(question.Prompt) == "" {
			return ToolResult{}, fmt.Errorf("question %d requires id and prompt", i+1)
		}
		if seen[question.ID] {
			return ToolResult{}, fmt.Errorf("duplicate question id %q", question.ID)
		}
		seen[question.ID] = true
		if len(question.Options) == 0 && question.AllowOther != nil && !*question.AllowOther {
			return ToolResult{}, fmt.Errorf("question %q has no available answer", question.ID)
		}
	}
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Text: "Waiting for the user to answer the questionnaire in the App.", Details: map[string]any{"questionnaire": request}}, nil
}

func decode(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return fmt.Errorf("tool arguments are empty")
	}
	return json.Unmarshal(raw, target)
}
func (t *toolset) path(value string) string {
	value = normalizeToolPath(value)
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(t.cwd, value)
}

func normalizeToolPath(value string) string {
	value = strings.NewReplacer("\u00a0", " ", "\u2000", " ", "\u2001", " ", "\u2002", " ", "\u2003", " ", "\u2004", " ", "\u2005", " ", "\u2006", " ", "\u2007", " ", "\u2008", " ", "\u2009", " ", "\u200a", " ", "\u202f", " ", "\u205f", " ", "\u3000", " ").Replace(value)
	value = strings.TrimPrefix(value, "@")
	if value == "~" || strings.HasPrefix(value, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if value == "~" {
				return home
			}
			return filepath.Join(home, value[2:])
		}
	}
	if strings.HasPrefix(value, "file://") {
		if parsed, err := url.Parse(value); err == nil {
			return parsed.Path
		}
	}
	return value
}
func (t *toolset) read(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  *int   `json:"limit"`
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, errors.New("Operation aborted")
	}
	data, err := os.ReadFile(t.path(p.Path))
	if err != nil {
		return ToolResult{}, err
	}
	if mime := imageMIME(p.Path, data); mime != "" {
		if len(data) > 20<<20 {
			return ToolResult{}, fmt.Errorf("image exceeds 20 MiB")
		}
		processed, processErr := processInlineImage(data, mime)
		if processErr != nil {
			return ToolResult{Text: "Read image file [" + mime + "]\n[" + processErr.Error() + "]"}, nil
		}
		text := "Read image file [" + processed.MIME + "]"
		if processed.Hint != "" {
			text += "\n" + processed.Hint
		}
		return ToolResult{Text: text, Images: []Image{{Data: processed.Data, MimeType: processed.MIME}}}, nil
	}
	lines := strings.Split(string(data), "\n")
	start := p.Offset
	if start < 1 {
		start = 1
	}
	end := len(lines)
	if p.Limit != nil {
		end = min(end, max(start-1, start-1+*p.Limit))
	}
	if start > len(lines) {
		return ToolResult{}, fmt.Errorf("Offset %d is beyond end of file (%d lines total)", p.Offset, len(lines))
	}
	selected := strings.Join(lines[start-1:end], "\n")
	truncated := truncateHead(selected, defaultMaxLines, defaultMaxBytes)
	if truncated.FirstLineExceeds {
		return ToolResult{Text: fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]", start, formatBytes(len([]byte(lines[start-1]))), formatBytes(defaultMaxBytes), start, p.Path, defaultMaxBytes), Details: map[string]any{"truncation": truncationDetails(truncated)}}, nil
	}
	output := truncated.Content
	if truncated.Truncated {
		next := start + truncated.OutputLines
		if truncated.By == "bytes" {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Use offset=%d to continue.]", start, next-1, len(lines), formatBytes(defaultMaxBytes), next)
		} else {
			output += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", start, next-1, len(lines), next)
		}
	} else if p.Limit != nil && end < len(lines) {
		output += fmt.Sprintf("\n\n[%d more lines in file. Use offset=%d to continue.]", len(lines)-end, end+1)
	}
	details := map[string]any{}
	if truncated.Truncated {
		details["truncation"] = truncationDetails(truncated)
	}
	return ToolResult{Text: output, Details: details}, nil
}
func imageMIME(path string, data []byte) string {
	if len(data) >= 12 {
		switch {
		case data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
			return "image/jpeg"
		case bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
			return "image/png"
		case bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a")):
			return "image/gif"
		case bytes.Equal(data[:2], []byte("BM")):
			return "image/bmp"
		case bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
			return "image/webp"
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	return ""
}
func (t *toolset) write(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p struct{ Path, Content string }
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	path := t.path(p.Path)
	unlock := lockFileMutation(path)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return ToolResult{}, errors.New("Operation aborted")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return ToolResult{}, err
	}
	if err := os.WriteFile(path, []byte(p.Content), 0644); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Text: fmt.Sprintf("Successfully wrote %d bytes to %s", len(utf16.Encode([]rune(p.Content))), p.Path)}, nil
}
func (t *toolset) edit(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p struct {
		Path    string          `json:"path"`
		OldText *string         `json:"oldText"`
		NewText *string         `json:"newText"`
		Edits   json.RawMessage `json:"edits"`
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	path := t.path(p.Path)
	edits, err := parseFileEdits(p.Edits, p.OldText, p.NewText)
	if err != nil {
		return ToolResult{}, err
	}
	unlock := lockFileMutation(path)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return ToolResult{}, errors.New("Operation aborted")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("could not edit file %s: %w", p.Path, err)
	}
	updated, diff, patch, firstLine, err := applyFileEdits(string(data), edits, p.Path)
	if err != nil {
		return ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, errors.New("Operation aborted")
	}
	if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Text: fmt.Sprintf("Successfully replaced %d block(s) in %s.", len(edits), p.Path), Details: map[string]any{"diff": diff, "patch": patch, "firstChangedLine": firstLine}}, nil
}
func (t *toolset) bash(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	return t.runBash(ctx, raw, nil)
}

func (t *toolset) gitRead(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var input struct {
		Operation string
		Revision  string
		Paths     []string
		Staged    bool
		MaxCount  int
	}
	if err := decode(raw, &input); err != nil {
		return ToolResult{}, err
	}
	for _, path := range input.Paths {
		if path == "" || len(path) > 4096 || strings.ContainsRune(path, 0) {
			return ToolResult{}, errors.New("git_read paths must be non-empty valid pathspecs")
		}
	}
	base := []string{"-c", "core.pager=cat", "-c", "pager.diff=false", "-c", "diff.external="}
	var args []string
	switch input.Operation {
	case "status":
		args = []string{"status", "--short", "--branch", "--untracked-files=all"}
	case "diff":
		args = []string{"diff", "--no-ext-diff", "--no-textconv"}
		if input.Staged {
			args = append(args, "--cached")
		}
	case "log":
		if input.MaxCount == 0 {
			input.MaxCount = 20
		}
		if input.MaxCount < 1 || input.MaxCount > 200 {
			return ToolResult{}, errors.New("git_read maxCount must be between 1 and 200")
		}
		args = []string{"log", "--no-ext-diff", "--no-textconv", fmt.Sprintf("--max-count=%d", input.MaxCount), "--format=fuller"}
	case "show":
		if input.Revision == "" {
			input.Revision = "HEAD"
		}
		validRevision := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@{}^~:+-]{0,255}$`)
		if !validRevision.MatchString(input.Revision) {
			return ToolResult{}, errors.New("git_read revision is invalid")
		}
		args = []string{"show", "--no-ext-diff", "--no-textconv", "--format=fuller", input.Revision}
	default:
		return ToolResult{}, errors.New("git_read operation must be status, diff, log, or show")
	}
	if len(input.Paths) > 0 {
		args = append(args, "--")
		args = append(args, input.Paths...)
	}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Dir = t.cwd
	cmd.Env = append(shellEnv(os.Environ()), "GIT_PAGER=cat", "GIT_TERMINAL_PROMPT=0", "GIT_EXTERNAL_DIFF=")
	output, err := cmd.CombinedOutput()
	text := limitOutput(output, 1<<20)
	if text == "" && err == nil {
		text = "(no output)"
	}
	result := ToolResult{Text: text, Details: map[string]any{"operation": input.Operation, "readOnly": true}}
	if err != nil {
		return result, fmt.Errorf("git_read %s failed: %w", input.Operation, err)
	}
	return result, nil
}

func (t *toolset) bashWithUpdates(ctx context.Context, raw json.RawMessage, onUpdate func(ToolResult) error) (ToolResult, error) {
	return t.runBash(ctx, raw, onUpdate)
}

func (t *toolset) runBash(ctx context.Context, raw json.RawMessage, onUpdate func(ToolResult) error) (ToolResult, error) {
	var p struct {
		Command string
		Timeout float64
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	if t.planMode && !safePlanCommand(p.Command) {
		return ToolResult{}, fmt.Errorf("Plan mode: command blocked (not allowlisted). Use /plan to disable plan mode first.\nCommand: %s", p.Command)
	}
	commandCtx := ctx
	cancel := func() {}
	if p.Timeout < 0 || p.Timeout > 2147483.647 {
		return ToolResult{}, fmt.Errorf("invalid timeout: must be greater than zero and at most 2147483.647 seconds")
	}
	if p.Timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, time.Duration(p.Timeout*float64(time.Second)))
	}
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "bash", "-lc", p.Command)
	cmd.Dir = t.cwd
	cmd.Env = shellEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	collector := &streamingOutput{onUpdate: onUpdate}
	cmd.Stdout, cmd.Stderr = collector, collector
	if err := cmd.Start(); err != nil {
		return ToolResult{}, err
	}
	done := make(chan struct{})
	go func(pid int) {
		select {
		case <-commandCtx.Done():
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		case <-done:
		}
	}(cmd.Process.Pid)
	err := cmd.Wait()
	close(done)
	output, updateErr := collector.result()
	truncated := truncateTail(output, defaultMaxLines, defaultMaxBytes)
	text := truncated.Content
	details := map[string]any{"truncation": truncationDetails(truncated)}
	if truncated.Truncated {
		fullOutputPath, persistErr := persistFullBashOutput(output)
		if persistErr != nil {
			return ToolResult{Text: text, Details: details}, persistErr
		}
		details["fullOutputPath"] = fullOutputPath
		startLine := truncated.TotalLines - truncated.OutputLines + 1
		if truncated.Partial {
			text += fmt.Sprintf("\n\n[Showing last %s of line %d. Full output: %s]", formatBytes(truncated.OutputBytes), truncated.TotalLines, fullOutputPath)
		} else if truncated.By == "lines" {
			text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Full output: %s]", startLine, truncated.TotalLines, truncated.TotalLines, fullOutputPath)
		} else {
			text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d (%s limit). Full output: %s]", startLine, truncated.TotalLines, truncated.TotalLines, formatBytes(defaultMaxBytes), fullOutputPath)
		}
	}
	if text == "" && err == nil {
		text = "(no output)"
	}
	if updateErr != nil {
		details["updateError"] = updateErr.Error()
		return ToolResult{Text: text, Details: details}, updateErr
	}
	if err != nil {
		details["exitError"] = err.Error()
		if commandCtx.Err() == context.DeadlineExceeded {
			return ToolResult{Text: text, Details: details}, errors.New(appendCommandStatus(text, fmt.Sprintf("Command timed out after %g seconds", p.Timeout)))
		}
		if commandCtx.Err() == context.Canceled {
			return ToolResult{Text: text, Details: details}, errors.New(appendCommandStatus(text, "Command aborted"))
		}
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			return ToolResult{Text: text, Details: details}, errors.New(appendCommandStatus(text, fmt.Sprintf("Command exited with code %d", exitError.ExitCode())))
		}
		return ToolResult{Text: text, Details: details}, err
	}
	return ToolResult{Text: text, Details: details}, nil
}

func appendCommandStatus(output, status string) string {
	if output == "" {
		return status
	}
	return output + "\n\n" + status
}

type streamingOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	onUpdate func(ToolResult) error
	err      error
}

func (w *streamingOutput) Write(data []byte) (int, error) {
	w.mu.Lock()
	_, _ = w.buffer.Write(data)
	current := truncateTail(w.buffer.String(), defaultMaxLines, defaultMaxBytes)
	callback := w.onUpdate
	w.mu.Unlock()
	if callback != nil {
		if err := callback(ToolResult{Text: current.Content, Details: map[string]any{"truncation": truncationDetails(current)}}); err != nil {
			w.mu.Lock()
			if w.err == nil {
				w.err = err
			}
			w.mu.Unlock()
		}
	}
	return len(data), nil
}

func (w *streamingOutput) result() (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.String(), w.err
}

func persistFullBashOutput(output string) (string, error) {
	file, err := os.CreateTemp("", "pi-bash-*.log")
	if err != nil {
		return "", fmt.Errorf("persist full bash output: %w", err)
	}
	path := file.Name()
	if _, err := io.WriteString(file, output); err != nil {
		_ = file.Close()
		return "", fmt.Errorf("persist full bash output: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("persist full bash output: %w", err)
	}
	return path, nil
}

func (t *toolset) grep(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p struct {
		Pattern, Path, Glob string
		IgnoreCase, Literal bool
		Context, Limit      int
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	searchPath := t.cwd
	if p.Path != "" {
		searchPath = t.path(p.Path)
	}
	info, err := os.Stat(searchPath)
	if err != nil {
		return ToolResult{}, fmt.Errorf("Path not found: %s", searchPath)
	}
	args := []string{"--json", "--line-number", "--color=never", "--hidden"}
	if p.IgnoreCase {
		args = append(args, "--ignore-case")
	}
	if p.Literal {
		args = append(args, "--fixed-strings")
	}
	if p.Glob != "" {
		args = append(args, "--glob", p.Glob)
	}
	args = append(args, "--", p.Pattern, searchPath)
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = t.cwd
	data, runErr := cmd.CombinedOutput()
	if runErr != nil {
		if ctx.Err() != nil {
			return ToolResult{}, errors.New("Operation aborted")
		}
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) || exitErr.ExitCode() != 1 {
			return ToolResult{}, fmt.Errorf("ripgrep failed: %s", strings.TrimSpace(string(data)))
		}
	}
	limit := p.Limit
	if limit < 1 {
		limit = 100
	}
	type match struct {
		path string
		line int
		text string
	}
	var matches []match
	for _, line := range splitCountLines(string(data)) {
		var event struct {
			Type string `json:"type"`
			Data struct {
				Path struct {
					Text string `json:"text"`
				} `json:"path"`
				Lines struct {
					Text string `json:"text"`
				} `json:"lines"`
				LineNumber int `json:"line_number"`
			} `json:"data"`
		}
		if json.Unmarshal([]byte(line), &event) == nil && event.Type == "match" && len(matches) < limit {
			matchPath := event.Data.Path.Text
			if !filepath.IsAbs(matchPath) {
				matchPath = filepath.Join(t.cwd, matchPath)
			}
			matches = append(matches, match{path: matchPath, line: event.Data.LineNumber, text: strings.TrimSuffix(normalizeLF(event.Data.Lines.Text), "\n")})
		}
	}
	if len(matches) == 0 {
		return ToolResult{Text: "No matches found"}, nil
	}
	limitReached := len(matches) >= limit
	linesTruncated := false
	var lines []string
	fileCache := map[string][]string{}
	formatPath := func(path string) string {
		if info.IsDir() {
			if relative, err := filepath.Rel(searchPath, path); err == nil && relative != "." && !strings.HasPrefix(relative, "..") {
				return filepath.ToSlash(relative)
			}
		}
		return filepath.Base(path)
	}
	for _, item := range matches {
		if p.Context <= 0 {
			text, wasTruncated := truncateSearchLine(item.text, 500)
			linesTruncated = linesTruncated || wasTruncated
			lines = append(lines, fmt.Sprintf("%s:%d: %s", formatPath(item.path), item.line, text))
			continue
		}
		fileLines, ok := fileCache[item.path]
		if !ok {
			content, readErr := os.ReadFile(item.path)
			if readErr == nil {
				fileLines = strings.Split(normalizeLF(string(content)), "\n")
			}
			fileCache[item.path] = fileLines
		}
		start, end := max(1, item.line-p.Context), min(len(fileLines), item.line+p.Context)
		for current := start; current <= end; current++ {
			text, wasTruncated := truncateSearchLine(fileLines[current-1], 500)
			linesTruncated = linesTruncated || wasTruncated
			separator := "-"
			if current == item.line {
				separator = ":"
			}
			lines = append(lines, fmt.Sprintf("%s%s%d%s %s", formatPath(item.path), separator, current, separator, text))
		}
	}
	rawOutput := strings.Join(lines, "\n")
	truncated := truncateHead(rawOutput, defaultMaxLines, defaultMaxBytes)
	output := truncated.Content
	var notices []string
	if limitReached {
		notices = append(notices, fmt.Sprintf("%d matches limit reached. Use limit=%d for more, or refine pattern", limit, limit*2))
	}
	if truncated.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", formatBytes(defaultMaxBytes)))
	}
	if linesTruncated {
		notices = append(notices, "Some lines truncated to 500 chars. Use read tool to see full lines")
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	details := map[string]any{}
	if limitReached {
		details["matchLimitReached"] = limit
	}
	if truncated.Truncated {
		details["truncation"] = truncationDetails(truncated)
	}
	if linesTruncated {
		details["linesTruncated"] = true
	}
	return ToolResult{Text: output, Details: details}, nil
}

func truncateSearchLine(value string, maxRunes int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value, false
	}
	return string(runes[:maxRunes]) + "...", true
}
func (t *toolset) find(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p struct {
		Pattern, Path string
		Limit         int
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	root := t.cwd
	if p.Path != "" {
		root = t.path(p.Path)
	}
	if _, err := os.Stat(root); err != nil {
		return ToolResult{}, fmt.Errorf("Path not found: %s", root)
	}
	limit := p.Limit
	if limit < 1 {
		limit = 1000
	}
	args := []string{"--glob", "--color=never", "--hidden"}
	insideGitRepo := false
	for current := root; ; current = filepath.Dir(current) {
		if _, statErr := os.Stat(filepath.Join(current, ".git")); statErr == nil {
			insideGitRepo = true
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	if !insideGitRepo {
		args = append(args, "--no-require-git")
	}
	args = append(args, "--max-results", strconv.Itoa(limit))
	effectivePattern := p.Pattern
	if strings.Contains(p.Pattern, "/") {
		args = append(args, "--full-path")
		if !strings.HasPrefix(p.Pattern, "/") && !strings.HasPrefix(p.Pattern, "**/") && p.Pattern != "**" {
			effectivePattern = "**/" + p.Pattern
		}
	}
	args = append(args, "--", effectivePattern, root)
	result, err := commandResult(ctx, t.cwd, "fd", args...)
	if err != nil {
		if ctx.Err() != nil {
			return ToolResult{}, errors.New("Operation aborted")
		}
		return result, err
	}
	output := strings.TrimSpace(result.Text)
	if output == "" {
		return ToolResult{Text: "No files found matching pattern"}, nil
	}
	lines := splitCountLines(output)
	for i, path := range lines {
		hadTrailingSlash := strings.HasSuffix(path, "/") || strings.HasSuffix(path, "\\")
		path = filepath.Clean(path)
		if relative, relativeErr := filepath.Rel(root, path); relativeErr == nil && relative != "." && !strings.HasPrefix(relative, "..") {
			path = relative
		}
		path = filepath.ToSlash(path)
		if hadTrailingSlash && !strings.HasSuffix(path, "/") {
			path += "/"
		}
		lines[i] = path
	}
	output = strings.Join(lines, "\n")
	truncated := truncateHead(output, 1<<30, defaultMaxBytes)
	details := map[string]any{}
	var notices []string
	if len(lines) >= limit {
		details["resultLimitReached"] = limit
		notices = append(notices, fmt.Sprintf("%d results limit reached. Use limit=%d for more, or refine pattern", limit, limit*2))
	}
	if truncated.Truncated {
		details["truncation"] = truncationDetails(truncated)
		notices = append(notices, fmt.Sprintf("%s limit reached", formatBytes(defaultMaxBytes)))
	}
	if len(notices) > 0 {
		truncated.Content += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	return ToolResult{Text: truncated.Content, Details: details}, nil
}
func (t *toolset) ls(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p struct {
		Path  string
		Limit int
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ToolResult{}, errors.New("Operation aborted")
	}
	path := t.cwd
	if p.Path != "" {
		path = t.path(p.Path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("Path not found: %s", path)
	}
	if !info.IsDir() {
		return ToolResult{}, fmt.Errorf("Not a directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return ToolResult{}, fmt.Errorf("Cannot read directory: %s", err.Error())
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		entryInfo, statErr := os.Stat(filepath.Join(path, name))
		if statErr != nil {
			continue
		}
		if entryInfo.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	limit := p.Limit
	if limit < 1 {
		limit = 500
	}
	limitReached := len(names) > limit
	if limitReached {
		names = names[:limit]
	}
	if len(names) == 0 {
		return ToolResult{Text: "(empty directory)"}, nil
	}
	truncated := truncateHead(strings.Join(names, "\n"), 1<<30, defaultMaxBytes)
	output := truncated.Content
	var notices []string
	if limitReached {
		notices = append(notices, fmt.Sprintf("%d entries limit reached. Use limit=%d for more", limit, limit*2))
	}
	if truncated.Truncated {
		notices = append(notices, fmt.Sprintf("%s limit reached", formatBytes(defaultMaxBytes)))
	}
	if len(notices) > 0 {
		output += "\n\n[" + strings.Join(notices, ". ") + "]"
	}
	details := map[string]any{}
	if limitReached {
		details["entryLimitReached"] = limit
	}
	if truncated.Truncated {
		details["truncation"] = truncationDetails(truncated)
	}
	return ToolResult{Text: output, Details: details}, nil
}
func (t *toolset) todo(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	var p struct {
		Action, Text string
		ID           int
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	if t.nextTodo == 0 {
		t.nextTodo = 1
	}
	switch p.Action {
	case "add":
		if p.Text == "" {
			return ToolResult{}, fmt.Errorf("text required for add")
		}
		t.todos = append(t.todos, todo{ID: t.nextTodo, Text: p.Text})
		t.nextTodo++
	case "toggle":
		found := false
		for i := range t.todos {
			if t.todos[i].ID == p.ID {
				t.todos[i].Done = !t.todos[i].Done
				found = true
			}
		}
		if !found {
			return ToolResult{}, fmt.Errorf("todo #%d not found", p.ID)
		}
	case "clear":
		t.todos = nil
		t.nextTodo = 1
	case "list":
	default:
		return ToolResult{}, fmt.Errorf("unknown todo action %q", p.Action)
	}
	var lines []string
	for _, item := range t.todos {
		mark := " "
		if item.Done {
			mark = "x"
		}
		lines = append(lines, "["+mark+"] #"+strconv.Itoa(item.ID)+": "+item.Text)
	}
	if len(lines) == 0 {
		lines = []string{"No todos"}
	}
	details := map[string]any{"action": p.Action, "todos": t.todos, "nextId": t.nextTodo}
	return ToolResult{Text: strings.Join(lines, "\n"), Details: details}, nil
}
func (t *toolset) runSubagent(ctx context.Context, raw json.RawMessage) (ToolResult, error) {
	type task struct{ Agent, Task, CWD string }
	type result struct {
		Agent  string `json:"agent"`
		Task   string `json:"task"`
		Output string `json:"output,omitempty"`
		Error  string `json:"error,omitempty"`
		Step   int    `json:"step,omitempty"`
	}
	var p struct {
		Agent, Task, CWD, AgentScope string
		Tasks, Chain                 []task
		ConfirmProjectAgents         *bool
	}
	if err := decode(raw, &p); err != nil {
		return ToolResult{}, err
	}
	if t.subagent == nil {
		return ToolResult{}, fmt.Errorf("subagents unavailable")
	}
	single, parallel, chain := p.Agent != "" && p.Task != "", len(p.Tasks) > 0, len(p.Chain) > 0
	modeCount := 0
	for _, enabled := range []bool{single, parallel, chain} {
		if enabled {
			modeCount++
		}
	}
	if modeCount != 1 {
		return ToolResult{}, fmt.Errorf("provide exactly one subagent mode")
	}
	scope := p.AgentScope
	if scope == "" {
		scope = t.subagentScope
	}
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "project" && scope != "both" {
		return ToolResult{}, fmt.Errorf("agentScope must be user, project, or both")
	}
	details := func(mode string, results []result) map[string]any {
		projectAgentsDir := ""
		if scope == "project" || scope == "both" {
			projectAgentsDir = nearest(t.cwd, filepath.Join(".pi", "agents"))
		}
		return map[string]any{"mode": mode, "agentScope": scope, "projectAgentsDir": projectAgentsDir, "results": results}
	}
	if single {
		text, err := t.subagent(ctx, p.Agent, p.Task, p.CWD, scope)
		item := result{Agent: p.Agent, Task: p.Task, Output: text}
		if err != nil {
			item.Error = err.Error()
			return ToolResult{Text: "Agent failed: " + err.Error(), Details: details("single", []result{item})}, err
		}
		if text == "" {
			text = "(no output)"
		}
		return ToolResult{Text: text, Details: details("single", []result{item})}, nil
	}
	if parallel {
		if len(p.Tasks) > 8 {
			text := fmt.Sprintf("Too many parallel tasks (%d). Max is 8.", len(p.Tasks))
			return ToolResult{Text: text, Details: details("parallel", []result{})}, nil
		}
		results := make([]result, len(p.Tasks))
		for offset := 0; offset < len(p.Tasks); offset += 4 {
			end := offset + 4
			if end > len(p.Tasks) {
				end = len(p.Tasks)
			}
			var wg sync.WaitGroup
			for i := offset; i < end; i++ {
				i := i
				wg.Add(1)
				go func() {
					defer wg.Done()
					text, err := t.subagent(ctx, p.Tasks[i].Agent, p.Tasks[i].Task, p.Tasks[i].CWD, scope)
					results[i] = result{Agent: p.Tasks[i].Agent, Task: p.Tasks[i].Task, Output: text}
					if err != nil {
						results[i].Error = err.Error()
					}
				}()
			}
			wg.Wait()
		}
		sections := make([]string, len(results))
		successes := 0
		for i, item := range results {
			status, output := "completed", item.Output
			if item.Error != "" {
				status, output = "failed", item.Error
			} else {
				successes++
			}
			if output == "" {
				output = "(no output)"
			}
			output = truncateSubagentOutput(output, 50*1024)
			sections[i] = fmt.Sprintf("### [%s] %s\n\n%s", item.Agent, status, output)
		}
		text := fmt.Sprintf("Parallel: %d/%d succeeded\n\n%s", successes, len(results), strings.Join(sections, "\n\n---\n\n"))
		return ToolResult{Text: text, Details: details("parallel", results)}, nil
	}
	previous := ""
	results := []result{}
	for index, step := range p.Chain {
		text, err := t.subagent(ctx, step.Agent, strings.ReplaceAll(step.Task, "{previous}", previous), step.CWD, scope)
		item := result{Agent: step.Agent, Task: step.Task, Output: text, Step: index + 1}
		if err != nil {
			item.Error = err.Error()
			results = append(results, item)
			message := fmt.Sprintf("Chain stopped at step %d (%s): %s", index+1, step.Agent, err.Error())
			return ToolResult{Text: message, Details: details("chain", results)}, err
		}
		results = append(results, item)
		previous = text
	}
	if previous == "" {
		previous = "(no output)"
	}
	return ToolResult{Text: previous, Details: details("chain", results)}, nil
}

func truncateSubagentOutput(output string, maximum int) string {
	data := []byte(output)
	if len(data) <= maximum {
		return output
	}
	end := maximum
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	omitted := len(data) - end
	return string(data[:end]) + fmt.Sprintf("\n\n[Output truncated: %d bytes omitted. Full output preserved in tool details.]", omitted)
}
func commandResult(ctx context.Context, cwd, name string, args ...string) (ToolResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = cwd
	out, err := cmd.CombinedOutput()
	text := limitOutput(out, 1<<20)
	if err != nil && len(out) == 0 {
		return ToolResult{}, err
	}
	return ToolResult{Text: text}, nil
}
func limitOutput(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	return string(data[:n]) + "\n... output truncated ..."
}
func shellEnv(values []string) []string {
	return append(append([]string(nil), values...), "AGENT_RUNTIME_SANDBOX=1")
}

// restrictedEnv is used for separately configured MCP child processes. Unlike
// the coding bash tool, an MCP server receives only its explicitly configured
// credentials and cannot inherit unrelated application/provider secrets.
func restrictedEnv(values []string) []string {
	secret := regexp.MustCompile(`(?i)(TOKEN|SECRET|PASSWORD|CREDENTIAL|API_KEY|_KEY$|_DSN$)`)
	out := make([]string, 0, len(values))
	for _, value := range values {
		key, _, _ := strings.Cut(value, "=")
		if secret.MatchString(key) || strings.HasPrefix(key, "AWS_CONTAINER_CREDENTIALS") {
			continue
		}
		out = append(out, value)
	}
	return append(out, "AGENT_RUNTIME_SANDBOX=1")
}

func safePlanCommand(command string) bool {
	destructive := []string{`(?i)\brm(dir)?\b`, `(?i)\b(mv|cp|mkdir|touch|chmod|chown|chgrp|ln|tee|truncate|dd|shred)\b`, `(^|[^<])>(?!>)`, `>>`, `(?i)\b(npm|yarn|pnpm|pip|apt|apt-get|brew)\s+(install|uninstall|update|ci|add|remove|publish|upgrade|purge)`, `(?i)\bgit\s+(add|commit|push|pull|merge|rebase|reset|checkout|branch\s+-[dD]|stash|cherry-pick|revert|tag|init|clone)`, `(?i)\b(sudo|su|kill|pkill|killall|reboot|shutdown)\b`, `(?i)\bsystemctl\s+(start|stop|restart|enable|disable)`, `(?i)\bservice\s+\S+\s+(start|stop|restart)`, `(?i)\b(vim?|nano|emacs|code|subl)\b`}
	for _, pattern := range destructive {
		// Go's regexp deliberately has no lookahead; handle the redirect rule separately.
		if strings.Contains(pattern, "?!") {
			if strings.Contains(command, ">") {
				return false
			}
			continue
		}
		if regexp.MustCompile(pattern).MatchString(command) {
			return false
		}
	}
	safe := []string{`^\s*(cat|head|tail|less|more|grep|find|ls|pwd|echo|printf|wc|sort|uniq|diff|file|stat|du|df|tree|which|whereis|type|env|printenv|uname|whoami|id|date|cal|uptime|ps|top|htop|free|jq|awk|rg|fd|bat|eza)\b`, `(?i)^\s*git\s+(status|log|diff|show|branch|remote|config\s+--get|ls-)`, `(?i)^\s*(npm|yarn)\s+(list|ls|view|info|search|outdated|audit|why)`, `(?i)^\s*(node|python)\s+--version`, `(?i)^\s*curl\s`, `(?i)^\s*wget\s+-O\s*-`, `(?i)^\s*sed\s+-n`}
	for _, pattern := range safe {
		if regexp.MustCompile(pattern).MatchString(command) {
			return true
		}
	}
	return false
}

var _ = io.Discard
