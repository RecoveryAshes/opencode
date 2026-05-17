package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type symbolInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

func lspTool(request Request) (Result, error) {
	operation, err := requireString(request.Params, "operation")
	if err != nil {
		return Result{}, err
	}
	filePath := optionalString(request.Params, "filePath", ".")
	file := resolvePath(request.Directory, filePath)
	switch operation {
	case "documentSymbol":
		symbols, err := documentSymbols(file)
		if err != nil {
			return Result{}, err
		}
		return jsonResult(operation+" "+filePath, symbols)
	case "workspaceSymbol":
		query := optionalString(request.Params, "query", "")
		symbols, err := workspaceSymbols(resolvePath(request.Directory, "."), query)
		if err != nil {
			return Result{}, err
		}
		return jsonResult(operation, symbols)
	case "hover":
		line := optionalInt(request.Params, "line", 1)
		text, err := lineAt(file, line)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Title: operation + " " + filePath,
			Metadata: map[string]any{
				"result": []map[string]any{{"contents": strings.TrimSpace(text), "line": line}},
			},
			Output: strings.TrimSpace(text),
		}, nil
	default:
		return Result{}, fmt.Errorf("no LSP server available for %s; fallback supports documentSymbol, workspaceSymbol, and hover", operation)
	}
}

func documentSymbols(file string) ([]symbolInfo, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read LSP file: %w", err)
	}
	language := languageForFile(file)
	patterns := symbolPatterns(language)
	lines := splitLines(string(data))
	result := []symbolInfo{}
	for index, line := range lines {
		for kind, re := range patterns {
			if match := re.FindStringSubmatch(line); len(match) > 1 {
				result = append(result, symbolInfo{Name: match[1], Kind: kind, Path: file, Line: index + 1})
				break
			}
		}
	}
	return result, nil
}

func workspaceSymbols(root string, query string) ([]symbolInfo, error) {
	result := []symbolInfo{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if ignoredOverviewDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if languageForFile(path) == "" {
			return nil
		}
		symbols, err := documentSymbols(path)
		if err != nil {
			return nil
		}
		for _, symbol := range symbols {
			if query == "" || strings.Contains(strings.ToLower(symbol.Name), strings.ToLower(query)) {
				result = append(result, symbol)
				if len(result) >= 200 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return result, err
}

func lineAt(file string, line int) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read LSP file: %w", err)
	}
	lines := splitLines(string(data))
	if line < 1 || line > len(lines) {
		return "", fmt.Errorf("line %d out of range", line)
	}
	return lines[line-1], nil
}

func jsonResult(title string, value any) (Result, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return Result{}, fmt.Errorf("encode LSP result: %w", err)
	}
	return Result{
		Title: title,
		Metadata: map[string]any{
			"result": value,
		},
		Output: string(data),
	}, nil
}

func languageForFile(file string) string {
	switch strings.ToLower(filepath.Ext(file)) {
	case ".go":
		return "go"
	case ".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs":
		return "typescript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".rb":
		return "ruby"
	default:
		return ""
	}
}

func symbolPatterns(language string) map[string]*regexp.Regexp {
	switch language {
	case "go":
		return map[string]*regexp.Regexp{
			"function": regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
			"type":     regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+`),
			"var":      regexp.MustCompile(`^\s*(?:var|const)\s+([A-Za-z_][A-Za-z0-9_]*)`),
		}
	case "typescript":
		return map[string]*regexp.Regexp{
			"function": regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)`),
			"class":    regexp.MustCompile(`^\s*(?:export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)`),
			"const":    regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][A-Za-z0-9_$]*)`),
		}
	case "python":
		return map[string]*regexp.Regexp{
			"function": regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
			"class":    regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)`),
		}
	case "rust":
		return map[string]*regexp.Regexp{
			"function": regexp.MustCompile(`^\s*(?:pub\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
			"type":     regexp.MustCompile(`^\s*(?:pub\s+)?(?:struct|enum|trait)\s+([A-Za-z_][A-Za-z0-9_]*)`),
		}
	case "ruby":
		return map[string]*regexp.Regexp{
			"function": regexp.MustCompile(`^\s*def\s+([A-Za-z_][A-Za-z0-9_!?=]*)`),
			"class":    regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_:]*)`),
		}
	default:
		return map[string]*regexp.Regexp{}
	}
}
