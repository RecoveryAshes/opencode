package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func projectList(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := store.ListProjects(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func projectInitGit(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := store.InitGit(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func projectByID(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPatch {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		id := strings.TrimPrefix(r.URL.Path, "/project/")
		if id == "" || strings.Contains(id, "/") {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid project id %q", id)
		}
		var payload integration.ProjectUpdateInput
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		result, err := store.UpdateProject(r.Context(), requestDirectory(r), id, payload)
		return result, statusFromGenericError(err), err
	})
}

func workspaceAdapters(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := store.WorkspaceAdapters(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func workspaceRoot(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		switch r.Method {
		case http.MethodGet:
			result, err := store.ListWorkspaces(r.Context(), requestDirectory(r))
			return result, statusFromGenericError(err), err
		case http.MethodPost:
			var payload integration.WorkspaceCreateInput
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, http.StatusBadRequest, err
			}
			result, err := store.CreateWorkspace(r.Context(), requestDirectory(r), payload)
			return result, statusFromGenericError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func workspaceSyncList(store *integration.WorkspaceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
			return
		}
		if err := store.SyncWorkspaces(r.Context(), requestDirectory(r)); err != nil {
			writeError(w, statusFromGenericError(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func workspaceStatus(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := store.WorkspaceStatuses(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func workspaceWarp(store *integration.WorkspaceStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
			return
		}
		var payload integration.WorkspaceWarpInput
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := store.WarpWorkspaceSession(r.Context(), payload); err != nil {
			writeError(w, statusFromGenericError(err), err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func workspaceByID(store *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodDelete {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		id := strings.TrimPrefix(r.URL.Path, "/experimental/workspace/")
		if id == "" || strings.Contains(id, "/") {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid workspace id %q", id)
		}
		result, err := store.RemoveWorkspace(r.Context(), requestDirectory(r), id)
		return result, statusFromGenericError(err), err
	})
}
