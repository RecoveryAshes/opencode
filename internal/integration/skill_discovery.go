package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const remoteSkillHTTPTimeout = 15 * time.Second

type remoteSkillIndex struct {
	Skills []remoteSkillEntry `json:"skills"`
}

type remoteSkillEntry struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
}

func configuredRemoteSkillRoots(info map[string]any) ([]string, error) {
	urls := configuredSkillURLs(info)
	if len(urls) == 0 {
		return nil, nil
	}
	result := []string{}
	for _, rawURL := range urls {
		dirs, err := pullRemoteSkills(rawURL)
		if err != nil {
			return nil, err
		}
		result = append(result, dirs...)
	}
	return uniqueStringsLocal(result), nil
}

func configuredSkillURLs(info map[string]any) []string {
	skills, ok := info["skills"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := skills["urls"].([]any)
	if !ok {
		return nil
	}
	result := []string{}
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

func pullRemoteSkills(rawURL string) ([]string, error) {
	base, err := remoteSkillBaseURL(rawURL)
	if err != nil {
		return []string{}, nil
	}
	indexURL := base.ResolveReference(&url.URL{Path: "index.json"})
	client := &http.Client{Timeout: remoteSkillHTTPTimeout}
	response, err := client.Get(indexURL.String())
	if err != nil {
		return []string{}, nil
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return []string{}, nil
	}
	var index remoteSkillIndex
	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&index); err != nil {
		return []string{}, nil
	}

	result := []string{}
	for _, skill := range index.Skills {
		if !validRemoteSkillEntry(skill) {
			continue
		}
		root := filepath.Join(globalCacheDir(), "skills", filepath.Clean(skill.Name))
		for _, file := range skill.Files {
			if !safeRemoteSkillFile(file) {
				continue
			}
			fileURL := base.ResolveReference(&url.URL{Path: skill.Name + "/" + file})
			dest := filepath.Join(root, filepath.FromSlash(file))
			if err := downloadRemoteSkillFile(client, fileURL.String(), dest); err != nil {
				continue
			}
		}
		if regularRemoteSkillFile(filepath.Join(root, "SKILL.md")) {
			result = append(result, root)
		}
	}
	return uniqueStringsLocal(result), nil
}

func remoteSkillBaseURL(rawURL string) (*url.URL, error) {
	text := strings.TrimSpace(rawURL)
	if text == "" {
		return nil, fmt.Errorf("empty skill url")
	}
	if !strings.HasSuffix(text, "/") {
		text += "/"
	}
	parsed, err := url.Parse(text)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid skill url")
	}
	return parsed, nil
}

func validRemoteSkillEntry(skill remoteSkillEntry) bool {
	name := strings.TrimSpace(skill.Name)
	if name == "" || name != skill.Name || strings.ContainsAny(name, `/\`+"\x00") || name == "." || name == ".." {
		return false
	}
	for _, file := range skill.Files {
		if file == "SKILL.md" {
			return true
		}
	}
	return false
}

func safeRemoteSkillFile(file string) bool {
	if file == "" || strings.HasPrefix(file, "/") || strings.Contains(file, `\`) || strings.ContainsRune(file, 0) {
		return false
	}
	cleaned := filepath.Clean(filepath.FromSlash(file))
	if cleaned == "." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) || cleaned == ".." {
		return false
	}
	return true
}

func downloadRemoteSkillFile(client *http.Client, rawURL string, dest string) error {
	if regularRemoteSkillFile(dest) {
		return nil
	}
	response, err := client.Get(rawURL)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download %s: status %d", rawURL, response.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	if _, err := io.Copy(file, response.Body); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

func globalCacheDir() string {
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
		return filepath.Join(cache, "opencode")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", "opencode")
	}
	return filepath.Join(".cache", "opencode")
}

func regularRemoteSkillFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
