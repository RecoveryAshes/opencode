package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func syncStart(store *integration.SyncStore, workspace *integration.WorkspaceStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := store.Start(r.Context(), requestDirectory(r))
		if err == nil {
			_ = workspace.SyncWorkspaces(r.Context(), requestDirectory(r))
		}
		return result, statusFromGenericError(err), err
	})
}

func syncReplay(store *integration.SyncStore, events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		var payload integration.SyncReplayInput
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		result, err := store.Replay(r.Context(), payload)
		if err == nil {
			events.publish("sync.replayed", map[string]any{
				"sessionID": result.SessionID,
				"events":    len(payload.Events),
			})
		}
		return result, statusFromGenericError(err), err
	})
}

func syncSteal(store *integration.SyncStore, events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		var payload integration.SyncSessionInput
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		workspaceID := workspaceIDFromRequest(r)
		result, err := store.Steal(r.Context(), workspaceID, payload.SessionID)
		if err == nil {
			events.publish("session.updated", map[string]any{
				"sessionID":   result.SessionID,
				"workspaceID": workspaceID,
			})
		}
		return result, statusFromGenericError(err), err
	})
}

func syncHistory(store *integration.SyncStore) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		var payload map[string]int64
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		result, err := store.History(r.Context(), payload)
		return result, statusFromGenericError(err), err
	})
}

func workspaceIDFromRequest(r *http.Request) string {
	for _, key := range []string{"workspace", "workspaceID", "workspace_id"} {
		value := strings.TrimSpace(r.URL.Query().Get(key))
		if value != "" {
			return value
		}
	}
	return strings.TrimSpace(r.Header.Get("x-opencode-workspace"))
}
