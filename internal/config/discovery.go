// Package config contains Go implementations of opencode local path and
// configuration discovery.
package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Discovery contains local opencode extension files discovered for a workspace.
type Discovery struct {
	Root     string   `json:"root"`
	Agents   []string `json:"agents"`
	Commands []string `json:"commands"`
	Skills   []string `json:"skills"`
	Themes   []string `json:"themes"`
}

// Discover walks the local `.opencode` directories that are already used by
// the TypeScript implementation for agents, commands, skills, and themes.
func Discover(root string) (Discovery, error) {
	root = filepath.Clean(root)
	result := Discovery{Root: root}

	var err error
	if result.Agents, err = markdownFiles(filepath.Join(root, ".opencode", "agent")); err != nil {
		return Discovery{}, err
	}
	if result.Commands, err = commandFileList(root); err != nil {
		return Discovery{}, err
	}
	if result.Skills, err = markdownFiles(filepath.Join(root, ".opencode", "skill")); err != nil {
		return Discovery{}, err
	}
	if result.Themes, err = jsonFiles(filepath.Join(root, ".opencode", "theme")); err != nil {
		return Discovery{}, err
	}
	return result, nil
}

func commandFileList(root string) ([]string, error) {
	files, err := commandFiles(root)
	if err != nil {
		return nil, err
	}
	return files, nil
}

func markdownFiles(dir string) ([]string, error) {
	return filesWithExtensions(dir, ".md", ".markdown")
}

func jsonFiles(dir string) ([]string, error) {
	return filesWithExtensions(dir, ".json", ".jsonc")
}

func filesWithExtensions(dir string, extensions ...string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}

	result := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if slices.Contains(extensions, strings.ToLower(filepath.Ext(entry.Name()))) {
			result = append(result, filepath.Join(dir, entry.Name()))
		}
	}
	slices.Sort(result)
	return result, nil
}
