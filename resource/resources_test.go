package resource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultCodingSystemPromptMatchesBundledPrompt(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "prompts", "system", "coding-agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != defaultCodingSystemPrompt {
		t.Fatal("built-in coding system prompt differs from prompts/system/coding-agent.md")
	}
}
