// Package agentassets exposes the SDK's embedded default runtime resources.
package agentassets

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

//go:embed agents prompts skills docs
var defaults embed.FS

var (
	defaultRootOnce sync.Once
	defaultRoot     string
	defaultRootErr  error
)

// DefaultRoot returns a process-stable filesystem directory containing the
// embedded Agent profiles, prompts, skills, and runtime documentation. A real
// directory is used because Agent tools and prompt instructions may need to
// read these files.
func DefaultRoot() (string, error) {
	defaultRootOnce.Do(materializeDefaults)
	return defaultRoot, defaultRootErr
}

func materializeDefaults() {
	root, err := os.MkdirTemp("", "ergo-agent-resources-*")
	if err != nil {
		defaultRootErr = err
		return
	}
	if err := fs.WalkDir(defaults, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := defaults.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	}); err != nil {
		_ = os.RemoveAll(root)
		defaultRootErr = err
		return
	}
	defaultRoot = root
}
