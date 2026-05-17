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
	Plugins  []string `json:"plugins"`
	Skills   []string `json:"skills"`
	Themes   []string `json:"themes"`
}

// Discover walks the local `.opencode` directories and external skill
// locations that are already used by the TypeScript implementation.
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
	if result.Plugins, err = pluginFileList(root); err != nil {
		return Discovery{}, err
	}
	if result.Skills, err = markdownFilesInDirs(
		filepath.Join(root, ".opencode", "skill"),
		filepath.Join(root, ".opencode", "skills"),
	); err != nil {
		return Discovery{}, err
	}
	externalSkills, err := externalSkillFiles(root, root)
	if err != nil {
		return Discovery{}, err
	}
	result.Skills = append(result.Skills, externalSkills...)
	slices.Sort(result.Skills)
	if result.Themes, err = jsonFilesInDirs(
		filepath.Join(root, ".opencode", "theme"),
		filepath.Join(root, ".opencode", "themes"),
	); err != nil {
		return Discovery{}, err
	}
	return result, nil
}

func pluginFileList(root string) ([]string, error) {
	return filesInDirs(
		[]string{".ts", ".js"},
		filepath.Join(root, ".opencode", "plugin"),
		filepath.Join(root, ".opencode", "plugins"),
	)
}

func externalSkillFiles(directory string, worktree string) ([]string, error) {
	dirs := []string{}
	for _, base := range externalSkillRoots() {
		if dirExists(base) {
			dirs = append(dirs, base)
		}
	}
	for _, dir := range dirsUp(directory, worktree) {
		for _, name := range []string{".agents", ".claude"} {
			candidate := filepath.Join(dir, name)
			if dirExists(candidate) {
				dirs = append(dirs, candidate)
			}
		}
	}
	return skillFilesUnderExternalDirs(dirs...)
}

func externalSkillRoots() []string {
	home := os.Getenv("OPENCODE_TEST_HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil
		}
	}
	return []string{
		filepath.Join(home, ".agents"),
		filepath.Join(home, ".claude"),
	}
}

func skillFilesUnderExternalDirs(dirs ...string) ([]string, error) {
	result := []string{}
	for _, dir := range dirs {
		files, err := recursiveSkillFiles(filepath.Join(dir, "skills"))
		if err != nil {
			return nil, err
		}
		result = append(result, files...)
	}
	slices.Sort(result)
	return result, nil
}

func recursiveSkillFiles(dir string) ([]string, error) {
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
		if entry.Name() == "SKILL.md" {
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
