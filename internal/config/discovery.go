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
	if result.Agents, err = markdownFilesInDirs(
		filepath.Join(root, ".opencode", "agent"),
		filepath.Join(root, ".opencode", "agents"),
	); err != nil {
		return Discovery{}, err
	}
	if result.Commands, err = commandFileList(root); err != nil {
		return Discovery{}, err
	}
	if result.Skills, err = markdownFilesInDirs(
		filepath.Join(root, ".opencode", "skill"),
		filepath.Join(root, ".opencode", "skills"),
	); err != nil {
		return Discovery{}, err
	}
	if result.Themes, err = jsonFilesInDirs(
		filepath.Join(root, ".opencode", "theme"),
		filepath.Join(root, ".opencode", "themes"),
	); err != nil {
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

func markdownFilesInDirs(dirs ...string) ([]string, error) {
	return filesInDirs([]string{".md", ".markdown"}, dirs...)
}

func jsonFilesInDirs(dirs ...string) ([]string, error) {
	return filesInDirs([]string{".json", ".jsonc"}, dirs...)
}

func filesInDirs(extensions []string, dirs ...string) ([]string, error) {
	result := []string{}
	for _, dir := range dirs {
		files, err := recursiveFilesWithExtensions(dir, extensions...)
		if err != nil {
			return nil, err
		}
		result = append(result, files...)
	}
	slices.Sort(result)
	return result, nil
}

func recursiveFilesWithExtensions(dir string, extensions ...string) ([]string, error) {
	if !dirExists(dir) {
		return []string{}, nil
	}
	result := []string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if slices.Contains(extensions, strings.ToLower(filepath.Ext(entry.Name()))) {
			result = append(result, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(result)
	return result, nil
}
