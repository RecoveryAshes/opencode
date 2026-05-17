package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func mcpRoot(manager *integration.MCPManager) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		switch r.Method {
		case http.MethodGet:
			return manager.Status(), http.StatusOK, nil
		case http.MethodPost:
			var payload struct {
				Name   string                `json:"name"`
				Config integration.MCPConfig `json:"config"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, http.StatusBadRequest, err
			}
			if payload.Name == "" {
				return nil, http.StatusBadRequest, fmt.Errorf("name is required")
			}
			return manager.Add(r.Context(), payload.Name, payload.Config), http.StatusOK, nil
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func mcpByName(manager *integration.MCPManager) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/mcp/"), "/")
		if len(parts) < 2 || parts[0] == "" {
			return nil, http.StatusNotFound, fmt.Errorf("unknown mcp route")
		}
		name := parts[0]
		switch {
		case len(parts) == 2 && parts[1] == "connect" && r.Method == http.MethodPost:
			if err := manager.Connect(r.Context(), name); err != nil {
				return nil, http.StatusBadRequest, err
			}
			return true, http.StatusOK, nil
		case len(parts) == 2 && parts[1] == "disconnect" && r.Method == http.MethodPost:
			manager.Disconnect(name)
			return true, http.StatusOK, nil
		case len(parts) == 2 && parts[1] == "tool" && r.Method == http.MethodGet:
			tools, err := manager.Tools(r.Context(), name)
			return tools, statusFromGenericError(err), err
		case len(parts) == 3 && parts[1] == "tool" && r.Method == http.MethodPost:
			var payload struct {
				Arguments map[string]any `json:"arguments"`
			}
			if r.ContentLength != 0 {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					return nil, http.StatusBadRequest, err
				}
			}
			result, err := manager.CallTool(r.Context(), name, parts[2], payload.Arguments)
			return result, statusFromGenericError(err), err
		default:
			return nil, http.StatusNotFound, fmt.Errorf("unknown mcp route")
		}
	})
}

func ptyRoot(manager *integration.PTYManager) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path == "/pty/shells" {
				return integration.Shells(), http.StatusOK, nil
			}
			return manager.List(), http.StatusOK, nil
		case http.MethodPost:
			var input integration.PTYCreateInput
			if r.ContentLength != 0 {
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					return nil, http.StatusBadRequest, err
				}
			}
			info, err := manager.Create(r.Context(), input)
			return info, statusFromGenericError(err), err
		default:
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
	})
}

func ptyByID(manager *integration.PTYManager) http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/pty/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			return nil, http.StatusNotFound, fmt.Errorf("unknown pty route")
		}
		id := parts[0]
		if len(parts) == 1 {
			switch r.Method {
			case http.MethodGet:
				info, ok := manager.Get(id)
				if !ok {
					return nil, http.StatusNotFound, fmt.Errorf("pty session not found")
				}
				return info, http.StatusOK, nil
			case http.MethodPut:
				var input integration.PTYUpdateInput
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					return nil, http.StatusBadRequest, err
				}
				info, ok := manager.Update(id, input)
				if !ok {
					return nil, http.StatusNotFound, fmt.Errorf("pty session not found")
				}
				return info, http.StatusOK, nil
			case http.MethodDelete:
				if !manager.Remove(id) {
					return nil, http.StatusNotFound, fmt.Errorf("pty session not found")
				}
				return true, http.StatusOK, nil
			default:
				return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
			}
		}
		if len(parts) == 2 && parts[1] == "connect-token" && r.Method == http.MethodPost {
			token, ok, err := manager.IssueConnectToken(id)
			if err != nil {
				return nil, http.StatusInternalServerError, err
			}
			if !ok {
				return nil, http.StatusNotFound, fmt.Errorf("pty session not found")
			}
			return token, http.StatusOK, nil
		}
		if len(parts) == 2 && parts[1] == "connect" && r.Method == http.MethodGet {
			if _, ok := manager.Get(id); !ok {
				return nil, http.StatusNotFound, fmt.Errorf("pty session not found")
			}
			ticket := r.URL.Query().Get("ticket")
			if ticket != "" && !manager.ConsumeConnectToken(id, ticket) {
				return nil, http.StatusForbidden, fmt.Errorf("invalid pty ticket")
			}
			return true, http.StatusOK, nil
		}
		if len(parts) == 2 && parts[1] == "input" && r.Method == http.MethodPost {
			var payload struct {
				Data string `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, http.StatusBadRequest, err
			}
			err := manager.Write(id, payload.Data)
			return true, statusFromGenericError(err), err
		}
		if len(parts) == 2 && parts[1] == "buffer" && r.Method == http.MethodGet {
			output, ok := manager.Buffer(id)
			if !ok {
				return nil, http.StatusNotFound, fmt.Errorf("pty session not found")
			}
			return map[string]any{"output": output}, http.StatusOK, nil
		}
		return nil, http.StatusNotFound, fmt.Errorf("unknown pty route")
	})
}

func statusFromGenericError(err error) int {
	if err == nil {
		return http.StatusOK
	}
	return http.StatusBadRequest
}
