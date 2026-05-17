package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type patchHunk struct {
	kind     string
	path     string
	movePath string
	contents string
	chunks   []patchChunk
}

type patchChunk struct {
	context string
	old     []string
	next    []string
	eof     bool
}

func applyPatchTool(request Request) (Result, error) {
	patchText, err := requireString(request.Params, "patchText")
	if err != nil {
		return Result{}, err
	}
	hunks, err := parsePatch(patchText)
	if err != nil {
		return Result{}, fmt.Errorf("apply_patch verification failed: %w", err)
	}
	if len(hunks) == 0 {
		if strings.TrimSpace(normalizeLineEndings(patchText)) == "*** Begin Patch\n*** End Patch" {
			return Result{}, fmt.Errorf("patch rejected: empty patch")
		}
		return Result{}, fmt.Errorf("apply_patch verification failed: no hunks found")
	}

	summary := []string{}
	files := []map[string]any{}
	for _, hunk := range hunks {
		target := resolvePath(request.Directory, hunk.path)
		switch hunk.kind {
		case "add":
			content := hunk.contents
			if content != "" && !strings.HasSuffix(content, "\n") {
				content += "\n"
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return Result{}, fmt.Errorf("create parent directory: %w", err)
			}
			if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
				return Result{}, fmt.Errorf("add file %s: %w", target, err)
			}
			summary = append(summary, "A "+relativeTitle(request.Directory, target))
			files = append(files, map[string]any{"filePath": target, "type": "add"})
		case "delete":
			if err := os.Remove(target); err != nil {
				return Result{}, fmt.Errorf("delete file %s: %w", target, err)
			}
			summary = append(summary, "D "+relativeTitle(request.Directory, target))
			files = append(files, map[string]any{"filePath": target, "type": "delete"})
		case "update":
			data, err := os.ReadFile(target)
			if err != nil {
				return Result{}, fmt.Errorf("apply_patch verification failed: Failed to read file to update: %s", target)
			}
			oldContent := strings.TrimPrefix(string(data), "\ufeff")
			newContent, err := deriveNewContents(target, oldContent, hunk.chunks)
			if err != nil {
				return Result{}, fmt.Errorf("apply_patch verification failed: %w", err)
			}
			writeTarget := target
			changeType := "update"
			if hunk.movePath != "" {
				writeTarget = resolvePath(request.Directory, hunk.movePath)
				changeType = "move"
			}
			if err := os.MkdirAll(filepath.Dir(writeTarget), 0o755); err != nil {
				return Result{}, fmt.Errorf("create parent directory: %w", err)
			}
			if err := os.WriteFile(writeTarget, []byte(newContent), 0o644); err != nil {
				return Result{}, fmt.Errorf("write file %s: %w", writeTarget, err)
			}
			if writeTarget != target {
				if err := os.Remove(target); err != nil {
					return Result{}, fmt.Errorf("remove moved file %s: %w", target, err)
				}
			}
			summary = append(summary, "M "+relativeTitle(request.Directory, writeTarget))
			additions, deletions := lineDelta(oldContent, newContent)
			files = append(files, map[string]any{
				"filePath":  target,
				"movePath":  nullableString(writeTarget, target),
				"type":      changeType,
				"additions": additions,
				"deletions": deletions,
				"patch":     simplePatch(writeTarget, oldContent, newContent),
			})
		}
	}
	output := "Success. Updated the following files:\n" + strings.Join(summary, "\n")
	return Result{
		Title: output,
		Metadata: map[string]any{
			"files": files,
		},
		Output: output,
	}, nil
}

func parsePatch(input string) ([]patchHunk, error) {
	cleaned := strings.TrimSpace(stripHeredoc(normalizeLineEndings(input)))
	lines := strings.Split(cleaned, "\n")
	begin := -1
	end := -1
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case "*** Begin Patch":
			begin = i
		case "*** End Patch":
			end = i
		}
	}
	if begin == -1 || end == -1 || begin >= end {
		return nil, fmt.Errorf("invalid patch format: missing Begin/End markers")
	}

	hunks := []patchHunk{}
	for i := begin + 1; i < end; {
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "*** Add File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Add File:"))
			if path == "" {
				return nil, fmt.Errorf("missing add file path")
			}
			content, next := parseAddContent(lines, i+1)
			hunks = append(hunks, patchHunk{kind: "add", path: path, contents: content})
			i = next
		case strings.HasPrefix(line, "*** Delete File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File:"))
			if path == "" {
				return nil, fmt.Errorf("missing delete file path")
			}
			hunks = append(hunks, patchHunk{kind: "delete", path: path})
			i++
		case strings.HasPrefix(line, "*** Update File:"):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Update File:"))
			if path == "" {
				return nil, fmt.Errorf("missing update file path")
			}
			i++
			movePath := ""
			if i < end && strings.HasPrefix(lines[i], "*** Move to:") {
				movePath = strings.TrimSpace(strings.TrimPrefix(lines[i], "*** Move to:"))
				i++
			}
			chunks, next := parsePatchChunks(lines, i)
			hunks = append(hunks, patchHunk{kind: "update", path: path, movePath: movePath, chunks: chunks})
			i = next
		default:
			i++
		}
	}
	return hunks, nil
}

func parseAddContent(lines []string, start int) (string, int) {
	var output strings.Builder
	i := start
	for i < len(lines) && !strings.HasPrefix(lines[i], "***") {
		if strings.HasPrefix(lines[i], "+") {
			output.WriteString(strings.TrimPrefix(lines[i], "+"))
			output.WriteByte('\n')
		}
		i++
	}
	return strings.TrimSuffix(output.String(), "\n"), i
}

func parsePatchChunks(lines []string, start int) ([]patchChunk, int) {
	chunks := []patchChunk{}
	i := start
	for i < len(lines) && !strings.HasPrefix(lines[i], "***") {
		if !strings.HasPrefix(lines[i], "@@") {
			i++
			continue
		}
		chunk := patchChunk{context: strings.TrimSpace(strings.TrimPrefix(lines[i], "@@"))}
		i++
		for i < len(lines) && !strings.HasPrefix(lines[i], "@@") && !strings.HasPrefix(lines[i], "***") {
			line := lines[i]
			switch {
			case line == "*** End of File":
				chunk.eof = true
			case strings.HasPrefix(line, " "):
				value := strings.TrimPrefix(line, " ")
				chunk.old = append(chunk.old, value)
				chunk.next = append(chunk.next, value)
			case strings.HasPrefix(line, "-"):
				chunk.old = append(chunk.old, strings.TrimPrefix(line, "-"))
			case strings.HasPrefix(line, "+"):
				chunk.next = append(chunk.next, strings.TrimPrefix(line, "+"))
			}
			i++
		}
		chunks = append(chunks, chunk)
	}
	return chunks, i
}

func deriveNewContents(filePath string, content string, chunks []patchChunk) (string, error) {
	lines := splitLines(normalizeLineEndings(content))
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	lineIndex := 0
	replacements := [][3]any{}
	for _, chunk := range chunks {
		if chunk.context != "" {
			contextIndex := seekSequence(lines, []string{chunk.context}, lineIndex, false)
			if contextIndex == -1 {
				return "", fmt.Errorf("failed to find context %q in %s", chunk.context, filePath)
			}
			lineIndex = contextIndex + 1
		}
		if len(chunk.old) == 0 {
			insertAt := len(lines)
			replacements = append(replacements, [3]any{insertAt, 0, chunk.next})
			continue
		}
		pattern := chunk.old
		next := chunk.next
		found := seekSequence(lines, pattern, lineIndex, chunk.eof)
		if found == -1 && len(pattern) > 0 && pattern[len(pattern)-1] == "" {
			pattern = pattern[:len(pattern)-1]
			if len(next) > 0 && next[len(next)-1] == "" {
				next = next[:len(next)-1]
			}
			found = seekSequence(lines, pattern, lineIndex, chunk.eof)
		}
		if found == -1 {
			return "", fmt.Errorf("failed to find expected lines in %s:\n%s", filePath, strings.Join(chunk.old, "\n"))
		}
		replacements = append(replacements, [3]any{found, len(pattern), next})
		lineIndex = found + len(pattern)
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		start := replacements[i][0].(int)
		length := replacements[i][1].(int)
		next := replacements[i][2].([]string)
		lines = append(lines[:start], append(next, lines[start+length:]...)...)
	}
	return strings.Join(ensureTrailingEmpty(lines), "\n"), nil
}

func seekSequence(lines []string, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 {
		return -1
	}
	compareFns := []func(string, string) bool{
		func(a string, b string) bool { return a == b },
		func(a string, b string) bool { return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t") },
		func(a string, b string) bool { return strings.TrimSpace(a) == strings.TrimSpace(b) },
		func(a string, b string) bool {
			return normalizePatchText(strings.TrimSpace(a)) == normalizePatchText(strings.TrimSpace(b))
		},
	}
	for _, compare := range compareFns {
		if eof {
			fromEnd := len(lines) - len(pattern)
			if fromEnd >= start && matchAt(lines, pattern, fromEnd, compare) {
				return fromEnd
			}
		}
		for i := start; i <= len(lines)-len(pattern); i++ {
			if matchAt(lines, pattern, i, compare) {
				return i
			}
		}
	}
	return -1
}

func matchAt(lines []string, pattern []string, start int, compare func(string, string) bool) bool {
	for i, item := range pattern {
		if !compare(lines[start+i], item) {
			return false
		}
	}
	return true
}

func normalizePatchText(value string) string {
	replacer := strings.NewReplacer("‘", "'", "’", "'", "“", `"`, "”", `"`, "–", "-", "—", "-", "…", "...", "\u00a0", " ")
	return replacer.Replace(value)
}

func stripHeredoc(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < 3 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "<<") {
		return input
	}
	marker := strings.Trim(strings.TrimPrefix(strings.TrimSpace(lines[0]), "<<"), `'"`)
	if marker == "" || strings.TrimSpace(lines[len(lines)-1]) != marker {
		return input
	}
	return strings.Join(lines[1:len(lines)-1], "\n")
}

func ensureTrailingEmpty(lines []string) []string {
	if len(lines) == 0 || lines[len(lines)-1] != "" {
		return append(lines, "")
	}
	return lines
}

func relativeTitle(base string, target string) string {
	if base == "" {
		return target
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}

func nullableString(value string, zero string) any {
	if value == zero {
		return nil
	}
	return value
}
