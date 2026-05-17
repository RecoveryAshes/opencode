package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/domain/session"
	"github.com/RecoveryAshes/opencode/internal/llm"
	"github.com/RecoveryAshes/opencode/internal/runtime"
)

type v2Cursor struct {
	ID        string `json:"id"`
	Time      int64  `json:"time"`
	Order     string `json:"order"`
	Direction string `json:"direction"`
}

type v2Page struct {
	Items  any       `json:"items"`
	Cursor v2Cursors `json:"cursor"`
}

type v2Cursors struct {
	Previous string `json:"previous,omitempty"`
	Next     string `json:"next,omitempty"`
}

func v2Sessions(repo session.Repository) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		limit, err := parseV2Limit(r.URL.Query().Get("limit"), 50)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		order := defaultString(r.URL.Query().Get("order"), "desc")
		cursor, err := decodeV2Cursor(r.URL.Query().Get("cursor"))
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		if cursor != nil {
			order = cursor.Order
		}
		items, err := repo.List(r.Context(), session.ListFilter{
			Search: r.URL.Query().Get("search"),
			Limit:  limit,
		})
		if err != nil {
			return nil, statusFromError(err), err
		}
		if order == "asc" {
			slices.Reverse(items)
		} else if order != "desc" {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid order %q", order)
		}
		items = applySessionCursor(items, cursor, limit)
		return v2Page{Items: items, Cursor: sessionCursors(items, order)}, http.StatusOK, nil
	})
}

func v2SessionByID(repo session.Repository, messages session.MessageRepository, promptRuntime *runtime.PromptRuntime, events *eventBus) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		sessionID, tail, err := parseV2SessionPath(r.URL.Path)
		if err != nil {
			return nil, http.StatusBadRequest, err
		}
		switch {
		case len(tail) == 1 && tail[0] == "message":
			return v2Messages(r, sessionID, messages)
		case len(tail) == 1 && tail[0] == "prompt":
			return v2Prompt(r, sessionID, messages, promptRuntime, events)
		case len(tail) == 1 && tail[0] == "compact":
			if r.Method != http.MethodPost {
				return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
			}
			now := session.NowMillis()
			_, err := repo.Update(r.Context(), sessionID, session.UpdateInput{Archived: nil, Summary: nil, ClearRevert: false})
			if err != nil {
				return nil, statusFromError(err), err
			}
			_ = now
			return map[string]any{}, http.StatusOK, nil
		case len(tail) == 1 && tail[0] == "wait":
			if r.Method != http.MethodPost {
				return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
			}
			return map[string]any{}, http.StatusOK, nil
		case len(tail) == 1 && tail[0] == "context":
			if r.Method != http.MethodGet {
				return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
			}
			result, err := messages.Messages(r.Context(), sessionID, 0)
			return flattenMessages(result), statusFromError(err), err
		default:
			return nil, http.StatusNotFound, fmt.Errorf("unknown v2 session route")
		}
	})
}

func v2Messages(r *http.Request, sessionID session.ID, messages session.MessageRepository) (any, int, error) {
	if r.Method != http.MethodGet {
		return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
	}
	limit, err := parseV2Limit(r.URL.Query().Get("limit"), 50)
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	order := defaultString(r.URL.Query().Get("order"), "desc")
	cursor, err := decodeV2Cursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if cursor != nil {
		order = cursor.Order
	}
	items, err := messages.Messages(r.Context(), sessionID, limit)
	if err != nil {
		return nil, statusFromError(err), err
	}
	if order == "desc" {
		slices.Reverse(items)
	} else if order != "asc" {
		return nil, http.StatusBadRequest, fmt.Errorf("invalid order %q", order)
	}
	items = applyMessageCursor(items, cursor, limit)
	flat := flattenMessages(items)
	return v2Page{Items: flat, Cursor: messageCursors(flat, order)}, http.StatusOK, nil
}

func v2Prompt(r *http.Request, sessionID session.ID, messages session.MessageRepository, promptRuntime *runtime.PromptRuntime, events *eventBus) (any, int, error) {
	if r.Method != http.MethodPost {
		return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
	}
	var payload struct {
		Prompt   any    `json:"prompt"`
		Delivery string `json:"delivery,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		return nil, http.StatusBadRequest, err
	}
	input := session.PromptInput{Agent: "build", Parts: []session.Part{{Type: "text", Data: map[string]any{"text": promptText(payload.Prompt)}}}}
	if payload.Delivery == "background" || payload.Delivery == "async" {
		input.NoReply = true
	}
	user, err := messages.CreatePrompt(r.Context(), sessionID, input)
	if err != nil {
		return nil, statusFromError(err), err
	}
	publishMessageEvents(events, sessionID, user)
	if input.NoReply || promptRuntime == nil {
		return flattenMessage(user), http.StatusOK, nil
	}
	assistant, err := promptRuntime.Reply(r.Context(), sessionID, user)
	if err != nil {
		return nil, statusFromError(err), err
	}
	publishMessageEvents(events, sessionID, assistant)
	return flattenMessage(user), http.StatusOK, nil
}

func v2Providers() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		cfg, err := loadRequestConfig(r)
		if err != nil {
			return nil, statusFromError(err), err
		}
		return llm.ListProviders(cfg.Info).All, http.StatusOK, nil
	})
}

func v2ProviderByID() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/provider/")
		if id == "" || strings.Contains(id, "/") {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid provider id")
		}
		cfg, err := loadRequestConfig(r)
		if err != nil {
			return nil, statusFromError(err), err
		}
		for _, provider := range llm.ListProviders(cfg.Info).All {
			if provider.ID == id {
				return provider, http.StatusOK, nil
			}
		}
		return nil, http.StatusNotFound, fmt.Errorf("provider not found")
	})
}

func v2Models() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		cfg, err := loadRequestConfig(r)
		if err != nil {
			return nil, statusFromError(err), err
		}
		result := []llm.PublicModel{}
		for _, provider := range llm.ListProviders(cfg.Info).All {
			for _, id := range sortedModelIDs(provider.Models) {
				result = append(result, provider.Models[id])
			}
		}
		return result, http.StatusOK, nil
	})
}

func parseV2Limit(value string, fallback int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	limit, err := parseLimit(value)
	if err != nil || limit < 1 || limit > 200 {
		return 0, fmt.Errorf("invalid limit %q", value)
	}
	return limit, nil
}

func parseV2SessionPath(path string) (session.ID, []string, error) {
	rest := strings.TrimPrefix(path, "/api/session/")
	if rest == path || rest == "" {
		return "", nil, fmt.Errorf("invalid v2 session path %q", path)
	}
	parts := strings.Split(rest, "/")
	id, err := session.ParseID(parts[0])
	if err != nil {
		return "", nil, err
	}
	return id, parts[1:], nil
}

func decodeV2Cursor(value string) (*v2Cursor, error) {
	if value == "" {
		return nil, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	var cursor v2Cursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, err
	}
	return &cursor, nil
}

func encodeV2Cursor(id string, created int64, order string, direction string) string {
	data, _ := json.Marshal(v2Cursor{ID: id, Time: created, Order: order, Direction: direction})
	return base64.RawURLEncoding.EncodeToString(data)
}

func sessionCursors(items []session.Info, order string) v2Cursors {
	if len(items) == 0 {
		return v2Cursors{}
	}
	first := items[0]
	last := items[len(items)-1]
	return v2Cursors{
		Previous: encodeV2Cursor(string(first.ID), first.Time.Created, order, "previous"),
		Next:     encodeV2Cursor(string(last.ID), last.Time.Created, order, "next"),
	}
}

func messageCursors(items []map[string]any, order string) v2Cursors {
	if len(items) == 0 {
		return v2Cursors{}
	}
	first := items[0]
	last := items[len(items)-1]
	return v2Cursors{
		Previous: encodeV2Cursor(stringValueMap(first, "id"), int64ValueMap(first, "created"), order, "previous"),
		Next:     encodeV2Cursor(stringValueMap(last, "id"), int64ValueMap(last, "created"), order, "next"),
	}
}

func applySessionCursor(items []session.Info, cursor *v2Cursor, limit int) []session.Info {
	if cursor == nil {
		return firstSessions(items, limit)
	}
	start := 0
	for index, item := range items {
		if string(item.ID) == cursor.ID {
			start = index + 1
			if cursor.Direction == "previous" {
				start = max(0, index-limit)
				return append([]session.Info{}, items[start:index]...)
			}
			break
		}
	}
	return firstSessions(items[start:], limit)
}

func applyMessageCursor(items []session.WithParts, cursor *v2Cursor, limit int) []session.WithParts {
	if cursor == nil {
		return firstMessages(items, limit)
	}
	start := 0
	for index, item := range items {
		if string(item.Info.ID) == cursor.ID {
			start = index + 1
			if cursor.Direction == "previous" {
				start = max(0, index-limit)
				return append([]session.WithParts{}, items[start:index]...)
			}
			break
		}
	}
	return firstMessages(items[start:], limit)
}

func firstSessions(items []session.Info, limit int) []session.Info {
	if len(items) <= limit {
		return append([]session.Info{}, items...)
	}
	return append([]session.Info{}, items[:limit]...)
}

func firstMessages(items []session.WithParts, limit int) []session.WithParts {
	if len(items) <= limit {
		return append([]session.WithParts{}, items...)
	}
	return append([]session.WithParts{}, items[:limit]...)
}

func flattenMessages(items []session.WithParts) []map[string]any {
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		result = append(result, flattenMessage(item))
	}
	return result
}

func flattenMessage(item session.WithParts) map[string]any {
	return map[string]any{
		"id":        item.Info.ID,
		"sessionID": item.Info.SessionID,
		"role":      item.Info.Role,
		"time":      item.Info.Time,
		"created":   item.Info.Time.Created,
		"agent":     item.Info.Agent,
		"model":     item.Info.Model,
		"parts":     item.Parts,
	}
}

func promptText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return text
		}
		if parts, ok := typed["parts"].([]any); ok {
			lines := []string{}
			for _, part := range parts {
				record, _ := part.(map[string]any)
				if text, ok := record["text"].(string); ok {
					lines = append(lines, text)
				}
			}
			return strings.Join(lines, "\n")
		}
	}
	return ""
}

func sortedModelIDs(models map[string]llm.PublicModel) []string {
	ids := make([]string, 0, len(models))
	for id := range models {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func stringValueMap(input map[string]any, key string) string {
	switch value := input[key].(type) {
	case string:
		return value
	case session.MessageID:
		return string(value)
	case session.ID:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}

func int64ValueMap(input map[string]any, key string) int64 {
	switch value := input[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
}
