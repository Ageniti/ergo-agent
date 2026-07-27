package agentassets

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestEmbeddedDefaultsMatchRepositoryResources(t *testing.T) {
	root, err := DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	again, err := DefaultRoot()
	if err != nil {
		t.Fatal(err)
	}
	if again != root {
		t.Fatalf("default root changed within process: %q != %q", again, root)
	}
	err = fs.WalkDir(defaults, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		want, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		if string(got) != string(want) {
			t.Errorf("embedded resource %s differs from repository source", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
