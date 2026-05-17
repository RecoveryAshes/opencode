package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteGlobGrepShellTools(t *testing.T) {
	root := t.TempDir()

	writeResult, err := Execute(context.Background(), Request{
		Name:      "write",
		Directory: root,
		Params: map[string]any{
			"filePath": filepath.Join("src", "main.go"),
			"content":  "package main\n\nfunc main() {\n\tprintln(\"needle\")\n}\n",
		},
	})
	if err != nil {
		t.Fatalf("write Execute() error = %v", err)
	}
	if writeResult.Output != "Wrote file successfully." {
		t.Fatalf("write output = %q", writeResult.Output)
	}

	readResult, err := Execute(context.Background(), Request{
		Name:      "read",
		Directory: root,
		Params:    map[string]any{"filePath": filepath.Join("src", "main.go")},
	})
	if err != nil {
		t.Fatalf("read Execute() error = %v", err)
	}
	if !strings.Contains(readResult.Output, "1: package main") || !strings.Contains(readResult.Output, "<type>file</type>") {
		t.Fatalf("read output = %q", readResult.Output)
	}

	globResult, err := Execute(context.Background(), Request{
		Name:      "glob",
		Directory: root,
		Params:    map[string]any{"pattern": "*.go", "path": "src"},
	})
	if err != nil {
		t.Fatalf("glob Execute() error = %v", err)
	}
	if !strings.Contains(globResult.Output, filepath.Join(root, "src", "main.go")) {
		t.Fatalf("glob output = %q", globResult.Output)
	}

	grepResult, err := Execute(context.Background(), Request{
		Name:      "grep",
		Directory: root,
		Params:    map[string]any{"pattern": "needle", "path": "src", "include": "*.go"},
	})
	if err != nil {
		t.Fatalf("grep Execute() error = %v", err)
	}
	if grepResult.Metadata["matches"] != 1 || !strings.Contains(grepResult.Output, "Line 4") {
		t.Fatalf("grep result = %#v", grepResult)
	}

	shellResult, err := Execute(context.Background(), Request{
		Name:      "shell",
		Directory: root,
		Params:    map[string]any{"command": "printf migrated"},
	})
	if err != nil {
		t.Fatalf("shell Execute() error = %v", err)
	}
	if shellResult.Metadata["exit"] != 0 || shellResult.Output != "migrated" {
		t.Fatalf("shell result = %#v", shellResult)
	}
}

func TestReadDirectoryAndBinaryRejection(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("write text fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.bin"), []byte{0, 1, 2, 3}, 0o644); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}

	result, err := Execute(context.Background(), Request{
		Name:      "read",
		Directory: root,
		Params:    map[string]any{"filePath": "."},
	})
	if err != nil {
		t.Fatalf("read directory Execute() error = %v", err)
	}
	if !strings.Contains(result.Output, "<type>directory</type>") || !strings.Contains(result.Output, "a.txt") {
		t.Fatalf("directory output = %q", result.Output)
	}

	_, err = Execute(context.Background(), Request{
		Name:      "read",
		Directory: root,
		Params:    map[string]any{"filePath": "data.bin"},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "binary") {
		t.Fatalf("binary read error = %v, want binary rejection", err)
	}
}
