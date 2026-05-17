package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func findText() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		pattern := r.URL.Query().Get("pattern")
		if pattern == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("pattern is required")
		}
		result, err := integration.FindText(requestDirectory(r), pattern, defaultFileLimit)
		return result, statusFromGenericError(err), err
	})
}

func findFile() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		query := r.URL.Query().Get("query")
		limit, err := parseFileLimit(r.URL.Query().Get("limit"))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		kind := r.URL.Query().Get("type")
		if kind != "" && kind != "file" && kind != "directory" {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid type %q", kind)
		}
		dirs := r.URL.Query().Get("dirs") != "false"
		result, err := integration.FindFilePaths(requestDirectory(r), query, dirs, kind, limit)
		return result, statusFromGenericError(err), err
	})
}

func findSymbol() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.FindSymbols(requestDirectory(r), r.URL.Query().Get("query"))
		return result, statusFromGenericError(err), err
	})
}

func fileList() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.ListFileNodes(requestDirectory(r), r.URL.Query().Get("path"))
		return result, statusFromGenericError(err), err
	})
}

func fileContent() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		path := r.URL.Query().Get("path")
		if path == "" {
			return nil, http.StatusBadRequest, fmt.Errorf("path is required")
		}
		result, err := integration.ReadFileContent(r.Context(), requestDirectory(r), path)
		return result, statusFromGenericError(err), err
	})
}

func fileStatus() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.FileStatus(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func requestDirectory(r *http.Request) string {
	return defaultString(r.URL.Query().Get("directory"), defaultString(r.Header.Get("x-opencode-directory"), "."))
}

func parseFileLimit(value string) (int, error) {
	if value == "" {
		return defaultFileLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 200 {
		return 0, fmt.Errorf("invalid limit %q", value)
	}
	return limit, nil
}

const defaultFileLimit = 10
