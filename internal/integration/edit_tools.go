package integration

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func editTool(request Request) (Result, error) {
	filePath, err := requireString(request.Params, "filePath")
	if err != nil {
		return Result{}, err
	}
	oldString, err := requireString(request.Params, "oldString")
	if err != nil {
		return Result{}, err
	}
	newString, err := requireString(request.Params, "newString")
	if err != nil {
		return Result{}, err
	}
	if oldString == newString {
		return Result{}, fmt.Errorf("no changes to apply: oldString and newString are identical")
	}

	target := resolvePath(request.Directory, filePath)
	existing, readErr := os.ReadFile(target)
	if oldString != "" && readErr != nil {
		return Result{}, fmt.Errorf("read file %s: %w", target, readErr)
	}
	if oldString == "" && readErr != nil && !os.IsNotExist(readErr) {
		return Result{}, fmt.Errorf("read file %s: %w", target, readErr)
	}

	bom := bytes.HasPrefix(existing, []byte{0xef, 0xbb, 0xbf})
	content := strings.TrimPrefix(string(existing), "\ufeff")
	ending := detectLineEnding(content)
	old := convertToLineEnding(normalizeLineEndings(oldString), ending)
	replacement := convertToLineEnding(normalizeLineEndings(newString), ending)

	var next string
	if oldString == "" {
		next = strings.TrimPrefix(newString, "\ufeff")
	} else if optionalBool(request.Params, "replaceAll", false) {
		if !strings.Contains(content, old) {
			return Result{}, fmt.Errorf("oldString not found in %s", target)
		}
		next = strings.ReplaceAll(content, old, replacement)
	} else {
		count := strings.Count(content, old)
		if count == 0 {
			return Result{}, fmt.Errorf("oldString not found in %s", target)
		}
		if count > 1 {
			return Result{}, fmt.Errorf("oldString appears %d times in %s; set replaceAll to true or provide more context", count, target)
		}
		next = strings.Replace(content, old, replacement, 1)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return Result{}, fmt.Errorf("create parent directory: %w", err)
	}
	outputBytes := []byte(next)
	if bom {
		outputBytes = append([]byte{0xef, 0xbb, 0xbf}, outputBytes...)
	}
	if err := os.WriteFile(target, outputBytes, 0o644); err != nil {
		return Result{}, fmt.Errorf("write file %s: %w", target, err)
	}

	additions, deletions := lineDelta(content, next)
	diff := simplePatch(target, content, next)
	return Result{
		Title: filepath.Base(target),
		Metadata: map[string]any{
			"filepath":  target,
			"diff":      diff,
			"additions": additions,
			"deletions": deletions,
		},
		Output: "Edit applied successfully.",
	}, nil
}

func normalizeLineEndings(text string) string {
	return strings.ReplaceAll(text, "\r\n", "\n")
}

func detectLineEnding(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func convertToLineEnding(text string, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

func optionalBool(params map[string]any, key string, fallback bool) bool {
	if value, ok := params[key].(bool); ok {
		return value
	}
	return fallback
}

func lineDelta(oldContent string, newContent string) (int, int) {
	oldLines := splitLines(normalizeLineEndings(oldContent))
	newLines := splitLines(normalizeLineEndings(newContent))
	additions := 0
	deletions := 0
	maxLen := max(len(oldLines), len(newLines))
	for i := range maxLen {
		var oldLine string
		var newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if oldLine == newLine {
			continue
		}
		if newLine != "" {
			additions++
		}
		if oldLine != "" {
			deletions++
		}
	}
	return additions, deletions
}

func simplePatch(filePath string, oldContent string, newContent string) string {
	oldLines := splitLines(normalizeLineEndings(oldContent))
	newLines := splitLines(normalizeLineEndings(newContent))
	var output strings.Builder
	output.WriteString("--- " + filePath + "\n")
	output.WriteString("+++ " + filePath + "\n")
	output.WriteString("@@ -1 +1 @@\n")
	maxLen := max(len(oldLines), len(newLines))
	for i := range maxLen {
		var oldLine string
		var newLine string
		if i < len(oldLines) {
			oldLine = oldLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if oldLine == newLine {
			if oldLine != "" {
				output.WriteString(" " + oldLine + "\n")
			}
			continue
		}
		if oldLine != "" {
			output.WriteString("-" + oldLine + "\n")
		}
		if newLine != "" {
			output.WriteString("+" + newLine + "\n")
		}
	}
	return strings.TrimRight(output.String(), "\n")
}
