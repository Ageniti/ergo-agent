package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestNewFSContainsOnlyApplicationResourcesAndRequiresAgentID(t *testing.T) {
	resourceFS := fstest.MapFS{
		"agents/only-meta.md": {
			Data: []byte("---\nname: only-meta\ndescription: Only test Agent\nrole: meta\ntools: read\nsystem-prompt: prompts/system/only-meta.md\n---\nOnly do the requested task.\n"),
		},
		"prompts/system/only-meta.md": {
			Data: []byte("You are only-meta.\n\n{{TOOLS}}\n{{GUIDELINES}}\n"),
		},
		"package.json": {
			Data: []byte(`{"name":"@test/only-meta","version":"0.1.0","pi":{"agents":["agents/*.md"]},"ergo":{"entryAgent":"only-meta","agentDependencies":[],"requiredTools":["read"],"optionalTools":[]}}`),
		},
	}

	agentRuntime, err := NewFS(resourceFS)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(agentRuntime.Resources.Root)

	definitions := agentRuntime.Resources.AgentDefinitionsAt("", "project")
	if len(definitions) != 1 || definitions[0].Name != "only-meta" {
		t.Fatalf("definitions=%+v", definitions)
	}
	if agentRuntime.DefaultAgentID != "" {
		t.Fatalf("minimal Runtime has default Agent %q", agentRuntime.DefaultAgentID)
	}
	err = agentRuntime.Run(context.Background(), map[string]any{
		"prompt": "test",
		"cwd":    ".",
	}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "agentId is required") {
		t.Fatalf("missing agentId error=%v", err)
	}
}

func TestNewHasNoImplicitAgent(t *testing.T) {
	agentRuntime := New(t.TempDir())
	if agentRuntime.DefaultAgentID != "" {
		t.Fatalf("minimal Runtime has default Agent %q", agentRuntime.DefaultAgentID)
	}
}
