package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportCoreContainsOnlyCoreSource(t *testing.T) {
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "ergo-core")
	if err := exportCore(sourceRoot, output); err != nil {
		t.Fatal(err)
	}

	goMod, err := os.ReadFile(filepath.Join(output, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(goMod), "module "+coreModule+"\n") {
		t.Fatalf("generated go.mod:\n%s", goMod)
	}
	readme, err := os.ReadFile(filepath.Join(output, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"# Ergo Core", "What is included", "What is excluded", coreModule + "/runtime"} {
		if !strings.Contains(string(readme), required) {
			t.Fatalf("generated README is missing %q:\n%s", required, readme)
		}
	}
	for _, forbidden := range []string{"agents", "prompts", "skills", "examples", "agent", "runner", "embed.go"} {
		if _, err := os.Stat(filepath.Join(output, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("core export contains %s: %v", forbidden, err)
		}
	}
	err = filepath.WalkDir(output, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if (filepath.Ext(path) == ".go" || filepath.Base(path) == "go.mod") &&
			strings.Contains(string(data), sourceModule) {
			t.Errorf("%s still references full Agent module", path)
		}
		if strings.Contains(string(data), "agents/chief-agent.md") ||
			strings.Contains(string(data), "prompts/system/chief-agent.md") {
			t.Errorf("%s contains a bundled Chief resource", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestExportCoreRefusesExistingOutput(t *testing.T) {
	err := exportCore(filepath.Join("..", ".."), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("err=%v", err)
	}
}
