package main

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ageniti/ergo-agent/resource"
)

func TestRunNewCreatesStandaloneAppWithoutChief(t *testing.T) {
	output := filepath.Join(t.TempDir(), "reviewer-agent")
	if err := runNew([]string{
		"--name", "reviewer-agent",
		"--role", "meta",
		"--output", output,
		"--module", "example.com/reviewer-agent",
		"--tools", "grep,read,grep",
	}); err != nil {
		t.Fatal(err)
	}

	mainPath := filepath.Join(output, "main.go")
	if _, err := parser.ParseFile(token.NewFileSet(), mainPath, nil, parser.AllErrors); err != nil {
		t.Fatalf("generated main.go: %v", err)
	}
	mainSource, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mainSource), "NewDefault") || strings.Contains(string(mainSource), "chief-agent") {
		t.Fatalf("generated app imports a default/Chief runtime:\n%s", mainSource)
	}
	readme, err := os.ReadFile(filepath.Join(output, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Standalone Ergo meta Agent", "github.com/ageniti/ergo-core/runtime", "reviewer-agent", "grep, read"} {
		if !strings.Contains(string(readme), required) {
			t.Fatalf("generated README is missing %q:\n%s", required, readme)
		}
	}
	readmeZH, err := os.ReadFile(filepath.Join(output, "README.zh-CN.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readmeZH), "独立 Ergo meta Agent") {
		t.Fatalf("generated Chinese README has unexpected content:\n%s", readmeZH)
	}

	resourceRoot := filepath.Join(output, "resources")
	if err := resource.ValidateAgentPackage(resourceRoot); err != nil {
		t.Fatalf("generated resources: %v", err)
	}
	definitions := (resource.Resources{Root: resourceRoot}).AgentDefinitionsAt("", "project")
	if len(definitions) != 1 || definitions[0].Name != "reviewer-agent" || definitions[0].Role != resource.AgentRoleMeta {
		t.Fatalf("definitions=%+v", definitions)
	}
	if _, err := os.Stat(filepath.Join(resourceRoot, "agents", "chief-agent.md")); !os.IsNotExist(err) {
		t.Fatalf("generated resources contain chief-agent: %v", err)
	}
}

func TestRunNewCreatesPackageOnlySubAgent(t *testing.T) {
	output := filepath.Join(t.TempDir(), "worker-agent")
	if err := runNew([]string{
		"--name", "worker-agent",
		"--role", "sub",
		"--output", output,
		"--package-only",
	}); err != nil {
		t.Fatal(err)
	}
	if err := resource.ValidateAgentPackage(output); err != nil {
		t.Fatalf("generated package: %v", err)
	}
	if _, err := os.Stat(filepath.Join(output, "main.go")); !os.IsNotExist(err) {
		t.Fatalf("package-only output unexpectedly contains main.go: %v", err)
	}
	for _, name := range []string{"README.md", "README.zh-CN.md"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("package-only output is missing %s: %v", name, err)
		}
	}

	data, err := os.ReadFile(filepath.Join(output, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Ergo struct {
			EntryAgent        string   `json:"entryAgent"`
			AgentDependencies []string `json:"agentDependencies"`
		} `json:"ergo"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Ergo.EntryAgent != "worker-agent" || len(manifest.Ergo.AgentDependencies) != 0 {
		t.Fatalf("manifest=%+v", manifest.Ergo)
	}
}

func TestRunNewRefusesExistingOutput(t *testing.T) {
	output := t.TempDir()
	err := runNew([]string{"--name", "test-agent", "--role", "meta", "--output", output})
	if err == nil || !strings.Contains(err.Error(), "output already exists") {
		t.Fatalf("err=%v", err)
	}
}
