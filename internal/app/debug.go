package app

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func debugCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode debug COMMAND")
		return 2
	}
	switch args[0] {
	case "file":
		return debugFileCommand(ctx, args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown debug command: %s\n", args[0])
		return 2
	}
}

func debugFileCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode debug file COMMAND")
		return 2
	}
	switch args[0] {
	case "read":
		return debugFileRead(ctx, args[1:], stdout, stderr)
	case "list":
		return debugFileList(args[1:], stdout, stderr)
	case "search":
		return debugFileSearch(args[1:], stdout, stderr)
	case "status":
		return debugFileStatus(ctx, args[1:], stdout, stderr)
	case "tree":
		return debugFileTree(args[1:], stdout, stderr)
	default:
		_, _ = fmt.Fprintf(stderr, "unknown debug file command: %s\n", args[0])
		return 2
	}
}

func debugFileRead(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug file read", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "project directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode debug file read [--directory DIR] PATH")
		return 2
	}
	content, err := integration.ReadFileContent(ctx, *directory, fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "read file failed: %v\n", err)
		return 1
	}
	return writeIndentedJSON(stdout, content)
}

func debugFileList(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug file list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "project directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode debug file list [--directory DIR] PATH")
		return 2
	}
	nodes, err := integration.ListFileNodes(*directory, fs.Arg(0))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "list files failed: %v\n", err)
		return 1
	}
	return writeIndentedJSON(stdout, nodes)
}

func debugFileSearch(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug file search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "project directory")
	limit := fs.Int("limit", 100, "maximum results")
	dirs := fs.Bool("dirs", true, "include directories")
	kind := fs.String("type", "", "result type: file or directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode debug file search [--directory DIR] [--limit N] [--dirs=false] [--type file|directory] QUERY")
		return 2
	}
	results, err := integration.FindFilePaths(*directory, fs.Arg(0), *dirs, *kind, *limit)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "search files failed: %v\n", err)
		return 1
	}
	for _, result := range results {
		if _, err := fmt.Fprintln(stdout, result); err != nil {
			return 1
		}
	}
	return 0
}

func debugFileStatus(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug file status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	directory := fs.String("directory", ".", "project directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode debug file status [--directory DIR]")
		return 2
	}
	status, err := integration.FileStatus(ctx, *directory)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "file status failed: %v\n", err)
		return 1
	}
	return writeIndentedJSON(stdout, status)
}

func debugFileTree(args []string, stdout io.Writer, stderr io.Writer) int {
	fs := flag.NewFlagSet("debug file tree", flag.ContinueOnError)
	fs.SetOutput(stderr)
	limit := fs.Int("limit", 200, "maximum directories to print")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := "."
	if fs.NArg() == 1 {
		dir = fs.Arg(0)
	} else if fs.NArg() > 1 {
		_, _ = fmt.Fprintln(stderr, "usage: opencode debug file tree [--limit N] [DIR]")
		return 2
	}
	tree, err := debugDirectoryTree(dir, *limit)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "tree failed: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(stdout, tree); err != nil {
		return 1
	}
	return 0
}

func writeIndentedJSON(stdout io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return 1
	}
	return 0
}

func debugDirectoryTree(root string, limit int) (string, error) {
	if limit <= 0 {
		limit = 200
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve tree root: %w", err)
	}
	files := []string{}
	if err := filepath.WalkDir(abs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == abs {
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if integration.FileAPIIgnored(rel) || strings.Contains(rel, ".opencode") {
				return filepath.SkipDir
			}
			return nil
		}
		if integration.FileAPIIgnored(rel) || strings.Contains(rel, ".opencode") {
			return nil
		}
		files = append(files, rel)
		return nil
	}); err != nil {
		return "", fmt.Errorf("walk tree: %w", err)
	}
	rootNode := debugTreeNode{Children: map[string]*debugTreeNode{}}
	for _, file := range files {
		parts := strings.Split(file, "/")
		if len(parts) < 2 {
			continue
		}
		node := &rootNode
		for _, part := range parts[:len(parts)-1] {
			child := node.Children[part]
			if child == nil {
				child = &debugTreeNode{Children: map[string]*debugTreeNode{}}
				node.Children[part] = child
			}
			node = child
		}
	}
	total := rootNode.count()
	lines := []string{}
	queue := rootNode.sortedChildren("")
	used := 0
	for i := 0; i < len(queue) && used < limit; i++ {
		item := queue[i]
		lines = append(lines, item.Path)
		used++
		queue = append(queue, item.Node.sortedChildren(item.Path)...)
	}
	if total > used {
		lines = append(lines, fmt.Sprintf("[%d truncated]", total-used))
	}
	return strings.Join(lines, "\n"), nil
}

type debugTreeNode struct {
	Children map[string]*debugTreeNode
}

func (node *debugTreeNode) count() int {
	total := 0
	for _, child := range node.Children {
		total += 1 + child.count()
	}
	return total
}

type debugTreeItem struct {
	Path string
	Node *debugTreeNode
}

func (node *debugTreeNode) sortedChildren(prefix string) []debugTreeItem {
	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		names = append(names, name)
	}
	slices.Sort(names)
	result := make([]debugTreeItem, 0, len(names))
	for _, name := range names {
		path := name
		if prefix != "" {
			path = prefix + "/" + name
		}
		result = append(result, debugTreeItem{Path: path, Node: node.Children[name]})
	}
	return result
}
