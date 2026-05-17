package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func experimentalToolList() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return integration.ToolList(), http.StatusOK, nil
	})
}

func experimentalToolIDs() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return integration.ToolIDs(), http.StatusOK, nil
	})
}

func experimentalWorktreeRoot(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		switch r.Method {
		case http.MethodGet:
			result, err := store.ListWorktreeDirectories(r.Context(), requestDirectory(r))
			return result, statusFromGenericError(err), err
		case http.MethodPost:
			var payload integration.WorktreeCreateInput
			if r.ContentLength != 0 {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					return nil, http.StatusBadRequest, err
				}
			}
			result, err := store.CreateWorktree(r.Context(), requestDirectory(r), payload)
			return result, statusFromGenericError(err), err
		case http.MethodDelete:
			var payload integration.WorktreeDirectoryInput
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, http.StatusBadRequest, err
			}
			result, err := store.RemoveWorktree(r.Context(), requestDirectory(r), payload)
			return result, statusFromGenericError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func experimentalWorktreeReset(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		var payload integration.WorktreeDirectoryInput
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		result, err := store.ResetWorktree(r.Context(), requestDirectory(r), payload)
		return result, statusFromGenericError(err), err
	})
}
