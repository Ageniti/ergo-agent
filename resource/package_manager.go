package resource

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
)

var packageSettingsMu sync.Mutex

// PackageManager is the Go-native resource package manager. It installs only
// declarative Pi resources (agents, skills, and prompts); executable JS
// extensions are deliberately outside the headless Go trust boundary.
type PackageManager struct {
	CWD                  string
	HTTPClient           *http.Client
	GitBinary            string
	AllowGlobalMutations bool
}

func (m PackageManager) Install(ctx context.Context, source, scope string, persist bool) (string, error) {
	scope, err := normalizePackageScope(scope)
	if err != nil {
		return "", err
	}
	if scope == "user" && !m.globalMutationsAllowed() {
		return "", errors.New("global package mutation is disabled; use project scope or enable AGENT_ALLOW_GLOBAL_PACKAGE_MUTATIONS")
	}
	item := PackageSource{Source: strings.TrimSpace(source)}
	base := packageScopeBase(m.CWD, scope)
	if item.Source == "" {
		return "", errors.New("package source is required")
	}
	if isLocalPackageSource(item.Source) {
		root := packageRoot(item, base)
		info, statErr := os.Stat(root)
		if statErr != nil || !info.IsDir() {
			return "", fmt.Errorf("local package does not exist: %s", root)
		}
		if err := ValidateAgentPackage(root); err != nil {
			return "", fmt.Errorf("validate local package: %w", err)
		}
		if persist {
			if err := mutatePackageSettings(m.CWD, scope, item.Source, true); err != nil {
				return "", err
			}
		}
		return root, nil
	}
	target := packageRoot(item, base)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(filepath.Dir(target), ".install-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temp)
	switch {
	case strings.HasPrefix(item.Source, "npm:"):
		err = m.installNPM(ctx, item.Source, temp)
	case isGitPackageSource(item.Source):
		err = m.installGit(ctx, item.Source, temp)
	default:
		err = fmt.Errorf("unsupported remote package source %q", item.Source)
	}
	if err != nil {
		return "", err
	}
	if err = ValidateAgentPackage(temp); err != nil {
		return "", fmt.Errorf("validate downloaded package: %w", err)
	}
	if err = replaceDirectory(target, temp); err != nil {
		return "", err
	}
	if persist {
		if err = mutatePackageSettings(m.CWD, scope, item.Source, true); err != nil {
			return "", err
		}
	}
	return target, nil
}

func (m PackageManager) Update(ctx context.Context, source, scope string) (string, error) {
	return m.Install(ctx, source, scope, false)
}

func (m PackageManager) Remove(source, scope string, persist bool) error {
	scope, err := normalizePackageScope(scope)
	if err != nil {
		return err
	}
	if scope == "user" && !m.globalMutationsAllowed() {
		return errors.New("global package mutation is disabled; use project scope or enable AGENT_ALLOW_GLOBAL_PACKAGE_MUTATIONS")
	}
	item := PackageSource{Source: strings.TrimSpace(source)}
	if !isLocalPackageSource(item.Source) {
		target := packageRoot(item, packageScopeBase(m.CWD, scope))
		root := filepath.Join(agentConfigDir(), "packages")
		if scope == "project" {
			root = filepath.Join(packageScopeBase(m.CWD, scope), ".pi", "packages")
		}
		if !inside(root, target) || filepath.Clean(target) == filepath.Clean(root) {
			return errors.New("refusing to remove package outside the managed package directory")
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if persist {
		return mutatePackageSettings(m.CWD, scope, item.Source, false)
	}
	return nil
}

func (m PackageManager) globalMutationsAllowed() bool {
	return m.AllowGlobalMutations || strings.EqualFold(strings.TrimSpace(os.Getenv("AGENT_ALLOW_GLOBAL_PACKAGE_MUTATIONS")), "true")
}

func (m PackageManager) installGit(ctx context.Context, source, target string) error {
	repository, ref := parseGitSource(source)
	binary := first(m.GitBinary, "git")
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, "--", repository, target)
	command := exec.CommandContext(ctx, binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone package: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m PackageManager) installNPM(ctx context.Context, source, target string) error {
	name, version, err := parseNPMSpec(source)
	if err != nil {
		return err
	}
	registry := strings.TrimRight(first(os.Getenv("AGENT_NPM_REGISTRY"), "https://registry.npmjs.org"), "/")
	metadataURL := registry + "/" + url.PathEscape(name)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return err
	}
	client := m.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("npm metadata returned %s", response.Status)
	}
	var metadata struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct {
				Tarball string `json:"tarball"`
			} `json:"dist"`
		} `json:"versions"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&metadata); err != nil {
		return err
	}
	resolvedVersion := metadata.DistTags[version]
	if resolvedVersion == "" {
		if _, exists := metadata.Versions[version]; exists {
			resolvedVersion = version
		}
	}
	if resolvedVersion == "" {
		constraint, constraintErr := semver.NewConstraint(version)
		if constraintErr != nil {
			return fmt.Errorf("invalid npm version or range %q: %w", version, constraintErr)
		}
		var selected *semver.Version
		for candidate := range metadata.Versions {
			parsed, parseErr := semver.NewVersion(candidate)
			if parseErr == nil && constraint.Check(parsed) && (selected == nil || parsed.GreaterThan(selected)) {
				selected = parsed
				resolvedVersion = candidate
			}
		}
	}
	tarball := metadata.Versions[resolvedVersion].Dist.Tarball
	if tarball == "" {
		return errors.New("npm package metadata has no tarball")
	}
	tarRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, tarball, nil)
	if err != nil {
		return err
	}
	archive, err := client.Do(tarRequest)
	if err != nil {
		return err
	}
	defer archive.Body.Close()
	if archive.StatusCode < 200 || archive.StatusCode >= 300 {
		return fmt.Errorf("npm tarball returned %s", archive.Status)
	}
	return extractPackageTarGZ(io.LimitReader(archive.Body, 256<<20), target)
}

func extractPackageTarGZ(source io.Reader, target string) error {
	const (
		maxPackageFiles               = 10_000
		maxPackageFileBytes     int64 = 128 << 20
		maxPackageExpandedBytes       = 512 << 20
	)
	gzipReader, err := gzip.NewReader(source)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	var files int
	var expanded int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(strings.TrimPrefix(filepath.ToSlash(header.Name), "package/"))
		if name == "." || name == "" {
			continue
		}
		path := filepath.Join(target, filepath.FromSlash(name))
		if !inside(target, path) {
			return fmt.Errorf("unsafe path in npm package: %s", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		//lint:ignore SA1019 npm tarballs may use the legacy NUL regular-file typeflag
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxPackageFileBytes {
				return fmt.Errorf("npm package file exceeds %d bytes: %s", maxPackageFileBytes, header.Name)
			}
			files++
			expanded += header.Size
			if files > maxPackageFiles || expanded > maxPackageExpandedBytes {
				return fmt.Errorf("npm package exceeds extraction limits (%d files, %d bytes)", maxPackageFiles, maxPackageExpandedBytes)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode)&0755)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func ExtractPackageTarGZ(source io.Reader, target string) error {
	return extractPackageTarGZ(source, target)
}

func replaceDirectory(target, staged string) error {
	backup := target + ".previous"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(staged, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	return os.RemoveAll(backup)
}

func mutatePackageSettings(cwd, scope, source string, add bool) error {
	packageSettingsMu.Lock()
	defer packageSettingsMu.Unlock()
	path := filepath.Join(agentConfigDir(), "settings.json")
	if scope == "project" {
		dir := nearest(cwd, ".pi")
		if dir == "" {
			dir = filepath.Join(cwd, ".pi")
		}
		path = filepath.Join(dir, "settings.json")
	}
	settings := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var packages []any
	if raw, ok := settings["packages"].([]any); ok {
		packages = raw
	}
	filtered := make([]any, 0, len(packages)+1)
	found := false
	for _, raw := range packages {
		existing := ""
		switch value := raw.(type) {
		case string:
			existing = value
		case map[string]any:
			existing, _ = value["source"].(string)
		}
		if existing == source {
			found = true
			if !add {
				continue
			}
		}
		filtered = append(filtered, raw)
	}
	if add && !found {
		filtered = append(filtered, source)
	}
	settings["packages"] = filtered
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err = temp.Write(append(data, '\n')); err == nil {
		err = temp.Sync()
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func normalizePackageScope(scope string) (string, error) {
	if scope == "" {
		scope = "user"
	}
	if scope != "user" && scope != "project" {
		return "", errors.New("package scope must be user or project")
	}
	return scope, nil
}

func packageScopeBase(cwd, scope string) string {
	if scope == "project" {
		if dir := nearest(cwd, ".pi"); dir != "" {
			return filepath.Dir(dir)
		}
		return cwd
	}
	return agentConfigDir()
}

func isLocalPackageSource(source string) bool {
	value := strings.TrimSpace(source)
	for _, prefix := range []string{"npm:", "git:", "github:", "http:", "https:", "ssh:"} {
		if strings.HasPrefix(value, prefix) {
			return false
		}
	}
	return true
}

func isGitPackageSource(source string) bool {
	value := strings.TrimPrefix(source, "git:")
	return strings.HasPrefix(source, "github:") || strings.HasPrefix(value, "git+") || strings.HasPrefix(value, "git@") || strings.HasPrefix(value, "ssh://") || strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")
}

func parseGitSource(source string) (string, string) {
	if strings.HasPrefix(source, "github:") {
		source = "https://github.com/" + strings.TrimPrefix(source, "github:")
	}
	value := strings.TrimPrefix(strings.TrimPrefix(source, "git:"), "git+")
	if index := strings.LastIndex(value, "#"); index >= 0 {
		return value[:index], value[index+1:]
	}
	return value, ""
}

func parseNPMSpec(source string) (string, string, error) {
	value := strings.TrimPrefix(strings.TrimSpace(source), "npm:")
	name, version := value, "latest"
	if strings.HasPrefix(value, "@") {
		if index := strings.LastIndex(value, "@"); index > strings.Index(value, "/") {
			name, version = value[:index], value[index+1:]
		}
	} else if index := strings.LastIndex(value, "@"); index > 0 {
		name, version = value[:index], value[index+1:]
	}
	if name == "" || strings.ContainsAny(name, `\ `) {
		return "", "", errors.New("invalid npm package source")
	}
	if version == "" {
		version = "latest"
	}
	return name, version, nil
}
