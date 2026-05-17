package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func questions(manager *integration.InteractionManager) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return manager.ListQuestions(), http.StatusOK, nil
	})
}

func questionByID(manager *integration.InteractionManager, events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		id, action, err := parseInteractionPath(r.URL.Path, "/question/")
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		switch action {
		case "reply":
			var payload struct {
				Answers []integration.QuestionAnswer `json:"answers"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, http.StatusBadRequest, err
			}
			request, ok := manager.ReplyQuestion(integration.QuestionID(id))
			if !ok {
				return nil, http.StatusNotFound, fmt.Errorf("question request not found")
			}
			events.publish("question.replied", map[string]any{
				"sessionID": request.SessionID,
				"requestID": request.ID,
				"answers":   payload.Answers,
			})
			return true, http.StatusOK, nil
		case "reject":
			request, ok := manager.RejectQuestion(integration.QuestionID(id))
			if !ok {
				return nil, http.StatusNotFound, fmt.Errorf("question request not found")
			}
			events.publish("question.rejected", map[string]any{
				"sessionID": request.SessionID,
				"requestID": request.ID,
			})
			return true, http.StatusOK, nil
		default:
			return nil, http.StatusNotFound, fmt.Errorf("unknown question route")
		}
	})
}

func permissions(manager *integration.InteractionManager) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return manager.ListPermissions(), http.StatusOK, nil
	})
}

func permissionByID(manager *integration.InteractionManager, events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		id, action, err := parseInteractionPath(r.URL.Path, "/permission/")
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		if action != "reply" {
			return nil, http.StatusNotFound, fmt.Errorf("unknown permission route")
		}
		var payload struct {
			Reply   string `json:"reply"`
			Message string `json:"message,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		if payload.Reply != "once" && payload.Reply != "always" && payload.Reply != "reject" {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid permission reply %q", payload.Reply)
		}
		request, ok := manager.ReplyPermission(integration.PermissionID(id))
		if !ok {
			return nil, http.StatusNotFound, fmt.Errorf("permission request not found")
		}
		events.publish("permission.replied", map[string]any{
			"sessionID": request.SessionID,
			"requestID": request.ID,
			"reply":     payload.Reply,
			"message":   payload.Message,
		})
		return true, http.StatusOK, nil
	})
}

func parseInteractionPath(path string, prefix string) (string, string, error) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path || rest == "" {
		return "", "", fmt.Errorf("invalid interaction path %q", path)
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid interaction path %q", path)
	}
	return parts[0], parts[1], nil
}
