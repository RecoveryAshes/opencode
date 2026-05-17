package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const defaultFileAPILimit = 10

// FileNode is the file browser node contract returned by the HTTP file API.
type FileNode struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Absolute string `json:"absolute"`
	Type     string `json:"type"`
	Ignored  bool   `json:"ignored"`
}

// FileInfo describes one git status row in the HTTP file API.
type FileInfo struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Status  string `json:"status"`
}

// FileContent is the stable content envelope returned by /file/content.
type FileContent struct {
	Type     string     `json:"type"`
	Content  string     `json:"content"`
	Diff     string     `json:"diff,omitempty"`
	Patch    *FilePatch `json:"patch,omitempty"`
	Encoding string     `json:"encoding,omitempty"`
	MimeType string     `json:"mimeType,omitempty"`
}

// FilePatch mirrors the optional patch shape from the TypeScript file contract.
type FilePatch struct {
	OldFileName string     `json:"oldFileName"`
	NewFileName string     `json:"newFileName"`
	OldHeader   string     `json:"oldHeader,omitempty"`
	NewHeader   string     `json:"newHeader,omitempty"`
	Hunks       []FileHunk `json:"hunks"`
	Index       string     `json:"index,omitempty"`
}

// FileHunk describes one structured diff hunk.
type FileHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"`
}

// SearchPathText is the nested ripgrep path text object.
type SearchPathText struct {
	Text string `json:"text"`
}

// SearchLineText is the nested ripgrep line text object.
type SearchLineText struct {
	Text string `json:"text"`
}

// SearchSubmatch is one ripgrep match span.
type SearchSubmatch struct {
	Match SearchLineText `json:"match"`
	Start int            `json:"start"`
	End   int            `json:"end"`
}

// SearchMatch mirrors the TypeScript Ripgrep.SearchMatch response shape.
type SearchMatch struct {
	Path           SearchPathText   `json:"path"`
	Lines          SearchLineText   `json:"lines"`
	LineNumber     int              `json:"line_number"`
	AbsoluteOffset int              `json:"absolute_offset"`
	Submatches     []SearchSubmatch `json:"submatches"`
}

// WorkspaceSymbol mirrors the LSP.Symbol shape used by find.symbol.
type WorkspaceSymbol struct {
	Name     string         `json:"name"`
	Kind     int            `json:"kind"`
	Location SymbolLocation `json:"location"`
}

// SymbolLocation identifies a symbol in a file.
type SymbolLocation struct {
	URI   string      `json:"uri"`
	Range SymbolRange `json:"range"`
}

// SymbolRange describes a zero-based LSP range.
type SymbolRange struct {
	Start SymbolPosition `json:"start"`
	End   SymbolPosition `json:"end"`
}

// SymbolPosition describes a zero-based LSP position.
type SymbolPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

var ignoredFileAPIFolders = map[string]bool{
	"node_modules":     true,
	"bower_components": true,
	".pnpm-store":      true,
	"vendor":           true,
	".npm":             true,
	"dist":             true,
	"build":            true,
	"out":              true,
	".next":            true,
	"target":           true,
	"bin":              true,
	"obj":              true,
	".git":             true,
	".svn":             true,
	".hg":              true,
	".vscode":          true,
	".idea":            true,
	".turbo":           true,
	".output":          true,
	"desktop":          true,
	".sst":             true,
	".cache":           true,
	".webkit-cache":    true,
	"__pycache__":      true,
	".pytest_cache":    true,
	"mypy_cache":       true,
	".history":         true,
	".gradle":          true,
}

var fileAPITextExtensions = map[string]bool{
	"ts": true, "tsx": true, "mts": true, "cts": true, "mtsx": true, "ctsx": true,
	"js": true, "jsx": true, "mjs": true, "cjs": true,
	"sh": true, "bash": true, "zsh": true, "fish": true, "ps1": true, "psm1": true, "cmd": true, "bat": true,
	"json": true, "jsonc": true, "json5": true, "yaml": true, "yml": true, "toml": true,
	"md": true, "mdx": true, "txt": true, "xml": true, "html": true, "htm": true,
	"css": true, "scss": true, "sass": true, "less": true, "graphql": true, "gql": true,
	"sql": true, "ini": true, "cfg": true, "conf": true, "env": true,
}

var fileAPITextNames = map[string]bool{
	"dockerfile": true, "makefile": true, ".gitignore": true, ".gitattributes": true,
	".editorconfig": true, ".npmrc": true, ".nvmrc": true, ".prettierrc": true, ".eslintrc": true,
}

var fileAPIImageExtensions = map[string]string{
	"png": "image/png", "jpg": "image/jpeg", "jpeg": "image/jpeg", "gif": "image/gif",
	"bmp": "image/bmp", "webp": "image/webp", "ico": "image/x-icon", "tif": "image/tiff",
	"tiff": "image/tiff", "svg": "image/svg+xml", "svgz": "image/svg+xml",
	"avif": "image/avif", "apng": "image/apng", "jxl": "image/jxl", "heic": "image/heic", "heif": "image/heif",
}

var fileAPIBinaryExtensions = map[string]bool{
	"exe": true, "dll": true, "pdb": true, "bin": true, "so": true, "dylib": true, "o": true, "a": true, "lib": true,
	"wav": true, "mp3": true, "ogg": true, "oga": true, "ogv": true, "ogx": true, "flac": true, "aac": true, "wma": true,
	"m4a": true, "weba": true, "mp4": true, "avi": true, "mov": true, "wmv": true, "flv": true, "webm": true, "mkv": true,
	"zip": true, "tar": true, "gz": true, "gzip": true, "bz": true, "bz2": true, "bzip": true, "bzip2": true,
	"7z": true, "rar": true, "xz": true, "lz": true, "z": true, "pdf": true, "doc": true, "docx": true,
	"ppt": true, "pptx": true, "xls": true, "xlsx": true, "dmg": true, "iso": true, "img": true, "vmdk": true,
	"ttf": true, "otf": true, "woff": true, "woff2": true, "eot": true, "sqlite": true, "db": true, "mdb": true,
	"apk": true, "ipa": true, "aab": true, "xapk": true, "app": true, "pkg": true, "deb": true, "rpm": true,
	"snap": true, "flatpak": true, "appimage": true, "msi": true, "msp": true, "jar": true, "war": true, "ear": true,
	"class": true, "kotlin_module": true, "dex": true, "vdex": true, "odex": true, "oat": true, "art": true,
	"wasm": true, "wat": true, "bc": true, "ll": true, "s": true, "ko": true, "sys": true, "drv": true,
	"efi": true, "rom": true, "com": true,
}

// ListFileNodes lists direct file and directory children under a project path.
func ListFileNodes(directory string, path string) ([]FileNode, error) {
	root, target, err := projectPath(directory, path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []FileNode{}, nil
		}
		return nil, fmt.Errorf("read directory %s: %w", target, err)
	}

	nodes := make([]FileNode, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" || entry.Name() == ".DS_Store" {
			continue
		}
		absolute := filepath.Join(target, entry.Name())
		rel := relSlash(root, absolute)
		kind := "file"
		ignoredPath := rel
		if entry.IsDir() {
			kind = "directory"
			ignoredPath += "/"
		}
		nodes = append(nodes, FileNode{
			Name:     entry.Name(),
			Path:     rel,
			Absolute: absolute,
			Type:     kind,
			Ignored:  FileAPIIgnored(ignoredPath),
		})
	}
	slices.SortFunc(nodes, func(a FileNode, b FileNode) int {
		if a.Type != b.Type {
			if a.Type == "directory" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	return nodes, nil
}

// ReadFileContent reads a project file with the TypeScript file content envelope.
func ReadFileContent(ctx context.Context, directory string, file string) (FileContent, error) {
	root, target, err := projectPath(directory, file)
	if err != nil {
		return FileContent{}, err
	}
	ext := cleanExtension(file)
	if mimeType, ok := fileAPIImageExtensions[ext]; ok {
		data, err := os.ReadFile(target)
		if errors.Is(err, os.ErrNotExist) {
			return FileContent{Type: "text", Content: ""}, nil
		}
		if err != nil {
			return FileContent{}, fmt.Errorf("read file %s: %w", target, err)
		}
		return FileContent{Type: "text", Content: base64.StdEncoding.EncodeToString(data), Encoding: "base64", MimeType: mimeType}, nil
	}

	knownText := fileAPITextExtensions[ext] || fileAPITextNames[strings.ToLower(filepath.Base(file))]
	if fileAPIBinaryExtensions[ext] && !knownText {
		return FileContent{Type: "binary", Content: ""}, nil
	}

	data, err := os.ReadFile(target)
	if errors.Is(err, os.ErrNotExist) {
		return FileContent{Type: "text", Content: ""}, nil
	}
	if err != nil {
		return FileContent{}, fmt.Errorf("read file %s: %w", target, err)
	}

	mimeType := detectMimeType(target, data)
	encode := !knownText && shouldEncodeFileContent(mimeType)
	if encode && !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return FileContent{Type: "binary", Content: "", MimeType: mimeType}, nil
	}
	if encode {
		return FileContent{
			Type:     "text",
			Content:  base64.StdEncoding.EncodeToString(data),
			Encoding: "base64",
			MimeType: mimeType,
		}, nil
	}
	if !knownText && isBinary(data) {
		return FileContent{Type: "binary", Content: "", MimeType: mimeType}, nil
	}

	result := FileContent{Type: "text", Content: strings.TrimSpace(string(data))}
	if isGitRepository(ctx, root) {
		diff := gitText(ctx, root, "-c", "core.fsmonitor=false", "diff", "--", filepath.ToSlash(file))
		if strings.TrimSpace(diff) == "" {
			diff = gitText(ctx, root, "-c", "core.fsmonitor=false", "diff", "--staged", "--", filepath.ToSlash(file))
		}
		if strings.TrimSpace(diff) != "" {
			result.Diff = diff
		}
	}
	return result, nil
}

// FileStatus returns git status rows for the project directory.
func FileStatus(ctx context.Context, directory string) ([]FileInfo, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return nil, err
	}
	if !isGitRepository(ctx, root) {
		return []FileInfo{}, nil
	}

	changed := []FileInfo{}
	diffOutput := gitText(ctx, root, "-c", "core.fsmonitor=false", "-c", "core.quotepath=false", "diff", "--numstat", "HEAD")
	for _, line := range nonEmptyLines(diffOutput) {
		parts := strings.Split(line, "\t")
		if len(parts) < 3 {
			continue
		}
		changed = append(changed, FileInfo{
			Path:    filepath.ToSlash(parts[2]),
			Added:   parseNumstat(parts[0]),
			Removed: parseNumstat(parts[1]),
			Status:  "modified",
		})
	}

	untrackedOutput := gitText(ctx, root, "-c", "core.fsmonitor=false", "-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard")
	for _, file := range nonEmptyLines(untrackedOutput) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			continue
		}
		changed = append(changed, FileInfo{
			Path:    filepath.ToSlash(file),
			Added:   len(splitLines(string(data))),
			Removed: 0,
			Status:  "added",
		})
	}

	deletedOutput := gitText(ctx, root, "-c", "core.fsmonitor=false", "-c", "core.quotepath=false", "diff", "--name-only", "--diff-filter=D", "HEAD")
	for _, file := range nonEmptyLines(deletedOutput) {
		changed = append(changed, FileInfo{Path: filepath.ToSlash(file), Added: 0, Removed: 0, Status: "deleted"})
	}
	return changed, nil
}

// FindFilePaths searches files or directories by fuzzy path match.
func FindFilePaths(directory string, query string, dirs bool, kind string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = defaultFileAPILimit
	}
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return nil, err
	}
	files, directories, err := scanFileAPIPaths(root)
	if err != nil {
		return nil, err
	}
	searchKind := kind
	if searchKind == "" {
		if dirs {
			searchKind = "all"
		} else {
			searchKind = "file"
		}
	}

	var items []string
	switch searchKind {
	case "file":
		items = files
	case "directory":
		items = directories
	default:
		items = append(append([]string{}, files...), directories...)
	}

	query = strings.TrimSpace(query)
	preferHidden := strings.HasPrefix(query, ".") || strings.Contains(query, "/.")
	if query == "" {
		if searchKind == "file" {
			return firstN(files, limit), nil
		}
		return firstN(sortHiddenLast(directories, preferHidden), limit), nil
	}

	matches := make([]string, 0, min(len(items), limit))
	lowerQuery := strings.ToLower(filepath.ToSlash(query))
	for _, item := range items {
		if fuzzyPathMatch(strings.ToLower(item), lowerQuery) {
			matches = append(matches, item)
		}
	}
	slices.SortFunc(matches, func(a string, b string) int {
		return comparePathSearch(a, b, lowerQuery, preferHidden)
	})
	if searchKind == "directory" {
		matches = sortHiddenLast(matches, preferHidden)
	}
	return firstN(matches, limit), nil
}

// FindText searches text files and returns ripgrep-compatible match rows.
func FindText(directory string, pattern string, limit int) ([]SearchMatch, error) {
	if limit <= 0 {
		limit = defaultFileAPILimit
	}
	if pattern == "" {
		return []SearchMatch{}, nil
	}
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return nil, err
	}
	matcher, err := compileSearchPattern(pattern)
	if err != nil {
		return nil, err
	}

	matches := []SearchMatch{}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		rel := relSlash(root, path)
		if entry.IsDir() {
			if FileAPIIgnored(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if FileAPIIgnored(rel) {
			return nil
		}
		fileMatches, err := searchFile(path, rel, matcher, limit-len(matches))
		if err != nil {
			return nil
		}
		matches = append(matches, fileMatches...)
		if len(matches) >= limit {
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search files: %w", err)
	}
	return matches, nil
}

// FindSymbols returns workspace symbols using the current local fallback scanner.
func FindSymbols(directory string, query string) ([]WorkspaceSymbol, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return nil, err
	}
	symbols, err := workspaceSymbols(root, query)
	if err != nil {
		return nil, err
	}
	result := make([]WorkspaceSymbol, 0, len(symbols))
	for _, symbol := range symbols {
		line := max(0, symbol.Line-1)
		result = append(result, WorkspaceSymbol{
			Name: symbol.Name,
			Kind: symbolKindNumber(symbol.Kind),
			Location: SymbolLocation{
				URI: "file://" + filepath.ToSlash(symbol.Path),
				Range: SymbolRange{
					Start: SymbolPosition{Line: line, Character: 0},
					End:   SymbolPosition{Line: line, Character: 0},
				},
			},
		})
	}
	return result, nil
}

// FileAPIIgnored reports whether a relative project path should be ignored by the file API.
func FileAPIIgnored(path string) bool {
	normalized := filepath.ToSlash(strings.Trim(path, "/"))
	if normalized == "" {
		return false
	}
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		if ignoredFileAPIFolders[part] {
			return true
		}
	}
	base := parts[len(parts)-1]
	switch {
	case base == ".DS_Store", base == "Thumbs.db":
		return true
	case strings.HasSuffix(base, ".swp"), strings.HasSuffix(base, ".swo"), strings.HasSuffix(base, ".pyc"), strings.HasSuffix(base, ".log"):
		return true
	}
	slashPath := "/" + normalized + "/"
	for _, segment := range []string{"/logs/", "/tmp/", "/temp/", "/coverage/", "/.nyc_output/"} {
		if strings.Contains(slashPath, segment) {
			return true
		}
	}
	return false
}

func projectPath(directory string, value string) (string, string, error) {
	root, err := cleanProjectRoot(directory)
	if err != nil {
		return "", "", err
	}
	target := value
	if target == "" {
		target = "."
	}
	if filepath.IsAbs(target) {
		target = filepath.Clean(target)
	} else {
		target = filepath.Clean(filepath.Join(root, target))
	}
	if !pathInside(root, target) {
		return "", "", fmt.Errorf("access denied: path escapes project directory")
	}
	return root, target, nil
}

func cleanProjectRoot(directory string) (string, error) {
	if directory == "" {
		directory = "."
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve directory: %w", err)
	}
	return filepath.Clean(root), nil
}

func pathInside(root string, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relSlash(root string, target string) string {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return filepath.ToSlash(target)
	}
	if rel == "." {
		return ""
	}
	return filepath.ToSlash(rel)
}

func cleanExtension(file string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(file)), ".")
}

func detectMimeType(path string, data []byte) string {
	if ext := strings.ToLower(filepath.Ext(path)); ext != "" {
		if mimeType := mime.TypeByExtension(ext); mimeType != "" {
			return mimeType
		}
	}
	if len(data) == 0 {
		return "text/plain; charset=utf-8"
	}
	return http.DetectContentType(data[:min(len(data), 512)])
}

func shouldEncodeFileContent(mimeType string) bool {
	mimeType = strings.ToLower(mimeType)
	if mimeType == "" || strings.HasPrefix(mimeType, "text/") || strings.Contains(mimeType, "charset=") {
		return false
	}
	top, _, _ := strings.Cut(mimeType, "/")
	switch top {
	case "image", "audio", "video", "font", "model", "multipart":
		return true
	default:
		return false
	}
}

func isGitRepository(ctx context.Context, root string) bool {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = root
	output, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

func gitText(ctx context.Context, root string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return string(output)
}

func nonEmptyLines(text string) []string {
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func parseNumstat(value string) int {
	if value == "-" {
		return 0
	}
	parsed, err := strconvAtoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func strconvAtoi(value string) (int, error) {
	result := 0
	if value == "" {
		return 0, fmt.Errorf("empty integer")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("invalid integer %q", value)
		}
		result = result*10 + int(r-'0')
	}
	return result, nil
}

func scanFileAPIPaths(root string) ([]string, []string, error) {
	files := []string{}
	dirSet := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		rel := relSlash(root, path)
		if entry.IsDir() {
			if FileAPIIgnored(rel) {
				return filepath.SkipDir
			}
			dirSet[rel+"/"] = true
			return nil
		}
		if FileAPIIgnored(rel) {
			return nil
		}
		files = append(files, rel)
		current := rel
		for {
			dir := filepath.ToSlash(filepath.Dir(current))
			if dir == "." || dir == "/" || dir == current {
				break
			}
			dirSet[dir+"/"] = true
			current = dir
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("scan files: %w", err)
	}
	directories := make([]string, 0, len(dirSet))
	for dir := range dirSet {
		directories = append(directories, dir)
	}
	slices.Sort(files)
	slices.Sort(directories)
	return files, directories, nil
}

func firstN(items []string, limit int) []string {
	if len(items) <= limit {
		return append([]string{}, items...)
	}
	return append([]string{}, items[:limit]...)
}

func sortHiddenLast(items []string, preferHidden bool) []string {
	if preferHidden {
		return append([]string{}, items...)
	}
	visible := []string{}
	hidden := []string{}
	for _, item := range items {
		if pathHasHiddenPart(item) {
			hidden = append(hidden, item)
		} else {
			visible = append(visible, item)
		}
	}
	return append(visible, hidden...)
}

func pathHasHiddenPart(path string) bool {
	for _, part := range strings.Split(strings.Trim(path, "/"), "/") {
		if strings.HasPrefix(part, ".") && len(part) > 1 {
			return true
		}
	}
	return false
}

func fuzzyPathMatch(item string, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(item, query) || strings.Contains(strings.ToLower(filepath.Base(item)), query) {
		return true
	}
	pos := 0
	for _, r := range item {
		if pos < len(query) && byte(r) == query[pos] {
			pos++
		}
	}
	return pos == len(query)
}

func comparePathSearch(a string, b string, query string, preferHidden bool) int {
	if !preferHidden {
		aHidden := pathHasHiddenPart(a)
		bHidden := pathHasHiddenPart(b)
		if aHidden != bHidden {
			if aHidden {
				return 1
			}
			return -1
		}
	}
	aScore := pathSearchScore(a, query)
	bScore := pathSearchScore(b, query)
	if aScore != bScore {
		return aScore - bScore
	}
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

func pathSearchScore(item string, query string) int {
	if strings.HasPrefix(strings.ToLower(filepath.Base(item)), query) {
		return 0
	}
	if strings.Contains(strings.ToLower(filepath.Base(item)), query) {
		return 1
	}
	if strings.Contains(item, query) {
		return 2
	}
	return 3
}

type lineMatcher interface {
	FindAllStringIndex(string, int) [][]int
}

func compileSearchPattern(pattern string) (lineMatcher, error) {
	return regexp.Compile(pattern)
}

func searchFile(path string, rel string, matcher lineMatcher, limit int) ([]SearchMatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil || isBinary(data) {
		return nil, nil
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	result := []SearchMatch{}
	lineNumber := 0
	offset := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineNumber++
		indexes := matcher.FindAllStringIndex(line, -1)
		if len(indexes) == 0 {
			offset += len(line) + 1
			continue
		}
		submatches := make([]SearchSubmatch, 0, len(indexes))
		for _, index := range indexes {
			submatches = append(submatches, SearchSubmatch{
				Match: SearchLineText{Text: line[index[0]:index[1]]},
				Start: index[0],
				End:   index[1],
			})
		}
		result = append(result, SearchMatch{
			Path:           SearchPathText{Text: rel},
			Lines:          SearchLineText{Text: line},
			LineNumber:     lineNumber,
			AbsoluteOffset: offset + indexes[0][0],
			Submatches:     submatches,
		})
		if len(result) >= limit {
			return result, nil
		}
		offset += len(line) + 1
	}
	return result, scanner.Err()
}

func symbolKindNumber(kind string) int {
	switch strings.ToLower(kind) {
	case "class":
		return 5
	case "method":
		return 6
	case "interface":
		return 11
	case "function":
		return 12
	case "var", "variable":
		return 13
	case "const", "constant":
		return 14
	case "type", "struct":
		return 23
	default:
		return 1
	}
}
