package resource

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestEveryBundledAgentBuildsAsAnIndependentPackage(t *testing.T) {
	resources := Resources{Root: filepath.Clean("..")}
	definitions := resources.AgentDefinitionsAt("", "project")
	if len(definitions) == 0 {
		t.Fatal("no bundled Agents found")
	}
	wantDependencies := map[string][]string{
		"chief-agent":    {"coding-agent", "planner", "reviewer", "scout", "web-researcher", "worker"},
		"coding-agent":   {"planner", "reviewer", "scout", "web-researcher", "worker"},
		"planner":        {},
		"reviewer":       {},
		"scout":          {},
		"web-researcher": {},
		"worker":         {},
	}
	parent := t.TempDir()
	for _, definition := range definitions {
		definition := definition
		t.Run(definition.Name, func(t *testing.T) {
			output := filepath.Join(parent, definition.Name)
			result, err := resources.BuildAgentPackage(output, AgentPackageBuildOptions{EntryAgent: definition.Name})
			if err != nil {
				t.Fatal(err)
			}
			want, exists := wantDependencies[definition.Name]
			if !exists {
				t.Fatalf("unexpected bundled Agent %q", definition.Name)
			}
			sort.Strings(want)
			if strings.Join(result.AgentDependencies, ",") != strings.Join(want, ",") {
				t.Fatalf("dependencies=%v want=%v", result.AgentDependencies, want)
			}
			if len(result.Agents) != len(want)+1 {
				t.Fatalf("packaged Agents=%v", result.Agents)
			}
			if _, err := os.Stat(filepath.Join(output, "prompts", "system", definition.Name+".md")); err != nil {
				t.Fatalf("packaged system prompt: %v", err)
			}
			for _, name := range []string{"README.md", "README.zh-CN.md"} {
				if _, err := os.Stat(filepath.Join(output, name)); err != nil {
					t.Fatalf("packaged documentation %s: %v", name, err)
				}
			}
			for _, name := range []string{"LICENSE", "LICENSE-COMMERCIAL.md", "NOTICE"} {
				if _, err := os.Stat(filepath.Join(output, name)); err != nil {
					t.Fatalf("packaged legal file %s: %v", name, err)
				}
			}
			loaded, err := (Resources{Root: output}).AgentAt(definition.Name, "", "project")
			if err != nil || loaded.Name != definition.Name {
				t.Fatalf("loaded=%+v err=%v", loaded, err)
			}
			if _, err := (Resources{Root: output}).BuildSystemPrompt(loaded, t.TempDir(), nil, false); err != nil {
				t.Fatalf("packaged system prompt is not self-contained: %v", err)
			}
			project := t.TempDir()
			if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
				t.Fatal(err)
			}
			settings, _ := json.Marshal(map[string]any{"packages": []string{output}})
			if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
				t.Fatal(err)
			}
			hostResources := Resources{Root: t.TempDir()}
			installed, err := hostResources.AgentAt(definition.Name, project, "project")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := hostResources.BuildSystemPrompt(installed, project, nil, false); err != nil {
				t.Fatalf("installed package resolved its system prompt through the host: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(output, "package.json"))
			if err != nil {
				t.Fatal(err)
			}
			var manifest struct {
				Pi struct {
					Agents []string `json:"agents"`
				} `json:"pi"`
				Ergo struct {
					EntryAgent        string   `json:"entryAgent"`
					AgentDependencies []string `json:"agentDependencies"`
					RequiredTools     []string `json:"requiredTools"`
				} `json:"ergo"`
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				t.Fatal(err)
			}
			if manifest.Ergo.EntryAgent != definition.Name || strings.Join(manifest.Ergo.AgentDependencies, ",") != strings.Join(want, ",") {
				t.Fatalf("manifest=%+v", manifest.Ergo)
			}
			if len(manifest.Pi.Agents) != 1 || manifest.Pi.Agents[0] != "agents/*.md" {
				t.Fatalf("pi.agents=%v", manifest.Pi.Agents)
			}
		})
	}
}

func TestAgentPackageBuilderRejectsMissingDelegateDependency(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	profile := "---\nname: broken\ndescription: broken package\nrole: sub\ntools: read, subagent\ndelegates: missing\n---\nBroken."
	if err := os.WriteFile(filepath.Join(root, "agents", "broken.md"), []byte(profile), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := (Resources{Root: root}).BuildAgentPackage(filepath.Join(t.TempDir(), "out"), AgentPackageBuildOptions{EntryAgent: "broken"})
	if err == nil || !strings.Contains(err.Error(), `missing dependency "missing"`) {
		t.Fatalf("err=%v", err)
	}
}

func TestBuiltAgentPackageOverridesBundledProfileAndFreezesWildcard(t *testing.T) {
	resources := Resources{Root: filepath.Clean("..")}
	output := filepath.Join(t.TempDir(), "chief-package")
	if _, err := resources.BuildAgentPackage(output, AgentPackageBuildOptions{EntryAgent: "chief-agent"}); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(output, "agents", "chief-agent.md")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `delegates: "*"`) {
		t.Fatal("wildcard delegate authority was not frozen")
	}
	if err := os.WriteFile(profilePath, append(data, []byte("\nPackage version marker.\n")...), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAgentPackage(output); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"packages": []string{output}})
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
		t.Fatal(err)
	}
	selected, err := resources.AgentAt("chief-agent", project, "project")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(selected.Body, "Package version marker.") || !strings.HasPrefix(selected.Path, output) {
		t.Fatalf("bundled profile shadowed package profile: %+v", selected)
	}
	if resources.IsBundledAgentAt("chief-agent", project, "project") {
		t.Fatal("same-name package profile was misclassified as bundled")
	}
}

func TestValidateAgentPackageRejectsMissingDependency(t *testing.T) {
	resources := Resources{Root: filepath.Clean("..")}
	output := filepath.Join(t.TempDir(), "coding-package")
	if _, err := resources.BuildAgentPackage(output, AgentPackageBuildOptions{EntryAgent: "coding-agent"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(output, "agents", "planner.md")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAgentPackage(output); err == nil || !strings.Contains(err.Error(), "planner") {
		t.Fatalf("missing dependency was accepted: %v", err)
	}
}

func TestValidateAgentPackageAcceptsLegacyChicoManifest(t *testing.T) {
	resources := Resources{Root: filepath.Clean("..")}
	output := filepath.Join(t.TempDir(), "legacy-package")
	if _, err := resources.BuildAgentPackage(output, AgentPackageBuildOptions{EntryAgent: "planner"}); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(output, "package.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(data), `"ergo":`, `"chico":`, 1)
	if legacy == string(data) {
		t.Fatal("generated package did not contain an ergo manifest")
	}
	if err := os.WriteFile(manifestPath, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAgentPackage(output); err != nil {
		t.Fatalf("legacy chico manifest was rejected: %v", err)
	}
}

func TestResolvePackageFileRejectsTraversalAndEscapingSymlink(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePackageFile(root, "../secret.md"); err == nil {
		t.Fatal("lexical package traversal was accepted")
	}
	link := filepath.Join(root, "prompt.md")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	if _, err := resolvePackageFile(root, "prompt.md"); err == nil {
		t.Fatal("package symlink escape was accepted")
	}
}

func TestPackageDiscoveryExcludesEscapingResourceSymlink(t *testing.T) {
	t.Setenv("AGENT_CONFIG_DIR", t.TempDir())
	project, pkg, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".pi"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "prompts"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"pi":{"prompts":["prompts/*.md"]}}`
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.md")
	if err := os.WriteFile(secret, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(pkg, "prompts", "escape.md")); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(map[string]any{"packages": []string{pkg}})
	if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0644); err != nil {
		t.Fatal(err)
	}
	resources := Resources{Root: t.TempDir()}
	for _, prompt := range resources.TemplatesAtScope(project, true) {
		if prompt.Name == "escape" {
			t.Fatal("escaping prompt symlink was loaded")
		}
	}
	diagnostics := resources.PackageDiagnostics(project)
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0].Message, "symlink escapes") {
		t.Fatalf("missing package symlink diagnostic: %+v", diagnostics)
	}
}

func TestAgentInPackageKeepsSameNameDependenciesVersionIsolated(t *testing.T) {
	resources := Resources{Root: filepath.Clean("..")}
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	for _, output := range []string{first, second} {
		if _, err := resources.BuildAgentPackage(output, AgentPackageBuildOptions{EntryAgent: "coding-agent"}); err != nil {
			t.Fatal(err)
		}
	}
	for root, marker := range map[string]string{first: "first planner", second: "second planner"} {
		path := filepath.Join(root, "agents", "planner.md")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, []byte("\n"+marker+"\n")...), 0644); err != nil {
			t.Fatal(err)
		}
	}
	firstPlanner, err := AgentInPackage(first, "planner")
	if err != nil {
		t.Fatal(err)
	}
	secondPlanner, err := AgentInPackage(second, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstPlanner.Body, "first planner") || !strings.Contains(secondPlanner.Body, "second planner") {
		t.Fatalf("package dependency versions crossed: first=%q second=%q", firstPlanner.Body, secondPlanner.Body)
	}
}
