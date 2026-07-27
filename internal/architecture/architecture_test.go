package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/ageniti/ergo-agent"

func TestSDKDependencyDirection(t *testing.T) {
	root := moduleRoot(t)
	allowed := map[string]map[string]bool{
		"":         {},
		"message":  {},
		"tool":     {modulePath + "/message": true},
		"session":  {},
		"provider": {modulePath + "/message": true, modulePath + "/tool": true},
		"resource": {modulePath + "/tool": true},
		"internal/engine": localImports(
			"internal/buildinfo", "message", "provider", "resource", "session", "tool",
		),
		"agent": localImports(
			"", "internal/engine", "message", "provider", "resource", "session", "tool",
		),
		"runner":     localImports("", "internal/engine"),
		"runtime":    localImports("internal/engine"),
		"extensions": localImports("internal/engine"),
	}

	for dir, permitted := range allowed {
		for _, imported := range productionImports(t, filepath.Join(root, dir)) {
			if imported != modulePath && !strings.HasPrefix(imported, modulePath+"/") {
				continue
			}
			if !permitted[imported] {
				t.Errorf("%s must not import %s", dir, imported)
			}
		}
	}
}

func TestAgentPackageRemainsACompatibilityFacade(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()
	entries, err := os.ReadDir(filepath.Join(root, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(root, "agent", entry.Name())
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch declaration := node.(type) {
			case *ast.TypeSpec:
				if !declaration.Assign.IsValid() {
					t.Errorf("%s: type %s must be an alias; implementations belong in focused packages or internal/engine", entry.Name(), declaration.Name)
				}
			case *ast.FuncDecl:
				if declaration.Recv != nil {
					t.Errorf("%s: method %s must not be implemented in the compatibility facade", entry.Name(), declaration.Name)
				}
			}
			return true
		})
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func productionImports(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var result []string
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, path)
		}
	}
	return result
}

func localImports(paths ...string) map[string]bool {
	result := make(map[string]bool, len(paths))
	for _, path := range paths {
		if path == "" {
			result[modulePath] = true
		} else {
			result[modulePath+"/"+path] = true
		}
	}
	return result
}
