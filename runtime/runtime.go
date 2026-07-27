// Package runtime provides the minimal Agent execution SDK.
//
// Unlike agent.NewDefault, this package does not import or embed Ergo's
// default Chief/Coding Agent resources. Applications must provide their own
// resource root and select an explicit agentId for every run.
package runtime

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	engine "github.com/ageniti/ergo-agent/internal/engine"
)

type (
	Runtime    = engine.Runtime
	Command    = engine.Command
	Event      = engine.Event
	EventSink  = engine.EventSink
	RunOptions = engine.RunOptions
)

const (
	RuntimeVersion      = engine.RuntimeVersion
	PromptBundleVersion = engine.PromptBundleVersion
)

// New constructs a Runtime containing only resources under root.
func New(root string) *Runtime {
	return engine.NewMinimal(root)
}

// NewFS materializes an application-owned resource filesystem and constructs a
// minimal Runtime from it. Only files present in resources are embedded.
func NewFS(resources fs.FS) (*Runtime, error) {
	root, err := os.MkdirTemp("", "ergo-agent-custom-resources-*")
	if err != nil {
		return nil, err
	}
	if err := fs.WalkDir(resources, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if name == "." {
			return nil
		}
		clean := filepath.Clean(filepath.FromSlash(name))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fs.ErrInvalid
		}
		target := filepath.Join(root, clean)
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := fs.ReadFile(resources, name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0644)
	}); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return engine.NewMinimal(root), nil
}
