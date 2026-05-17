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

type lspLocation struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Preview   string `json:"preview,omitempty"`
}

func lspTool(request Request) (Result, error) {
	operation, err := requireString(request.Params, "operation")
	if err != nil {
		return Result{}, err
	}
	filePath := optionalString(request.Params, "filePath", ".")
	file := resolvePath(request.Directory, filePath)
	line := optionalInt(request.Params, "line", 1)
	character := optionalInt(request.Params, "character", 1)
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
	case "goToDefinition", "goToImplementation", "prepareCallHierarchy":
		result, err := definitionLikeLocations(file, line, character)
		if err != nil {
			return Result{}, err
		}
		return jsonResult(operation+" "+filePath, result)
	case "findReferences", "incomingCalls", "outgoingCalls":
		result, err := referenceLikeLocations(resolvePath(request.Directory, "."), file, line, character)
		if err != nil {
			return Result{}, err
		}
		return jsonResult(operation+" "+filePath, result)
	default:
		return Result{}, fmt.Errorf("unsupported LSP operation %q", operation)
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

func definitionLikeLocations(file string, line int, character int) ([]lspLocation, error) {
	ident, err := identifierAt(file, line, character)
	if err != nil {
		return nil, err
	}
	if ident == "" {
		return []lspLocation{}, nil
	}
	symbols, err := documentSymbols(file)
	if err != nil {
		return nil, err
	}
	result := []lspLocation{}
	for _, symbol := range symbols {
		if symbol.Name != ident {
			continue
		}
		preview, _ := lineAt(symbol.Path, symbol.Line)
		result = append(result, lspLocation{
			Path:      symbol.Path,
			Line:      symbol.Line,
			Character: 1,
			Preview:   strings.TrimSpace(preview),
		})
	}
	return result, nil
}

func referenceLikeLocations(root string, file string, line int, character int) ([]lspLocation, error) {
	ident, err := identifierAt(file, line, character)
	if err != nil {
		return nil, err
	}
	if ident == "" {
		return []lspLocation{}, nil
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(ident) + `\b`)
	result := []lspLocation{}
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
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
		data, err := os.ReadFile(path)
		if err != nil || isBinary(data) {
			return nil
		}
		for index, text := range splitLines(string(data)) {
			for _, loc := range re.FindAllStringIndex(text, -1) {
				result = append(result, lspLocation{
					Path:      path,
					Line:      index + 1,
					Character: loc[0] + 1,
					Preview:   strings.TrimSpace(text),
				})
				if len(result) >= 200 {
					return filepath.SkipAll
				}
			}
		}
		return nil
	})
	return result, err
}

func identifierAt(file string, line int, character int) (string, error) {
	text, err := lineAt(file, line)
	if err != nil {
		return "", err
	}
	if character < 1 {
		character = 1
	}
	runes := []rune(text)
	index := min(character-1, len(runes))
	start := index
	if start == len(runes) && start > 0 {
		start--
	}
	for start > 0 && isIdentifierRune(runes[start-1]) {
		start--
	}
	end := index
	for end < len(runes) && isIdentifierRune(runes[end]) {
		end++
	}
	if end <= start {
		return "", nil
	}
	return string(runes[start:end]), nil
}

func isIdentifierRune(r rune) bool {
	return r == '_' || r == '$' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
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
