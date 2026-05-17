package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/RecoveryAshes/opencode/internal/integration"
)

func instanceDispose() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		return true, http.StatusOK, nil
	})
}

func instancePath() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.InstancePathInfo(requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func vcsInfo() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.VCS(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func vcsStatus() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.VCSStatus(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func vcsDiff() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		mode := r.URL.Query().Get("mode")
		if mode == "" {
			mode = "git"
		}
		if mode != "git" && mode != "branch" {
			return nil, http.StatusBadRequest, fmt.Errorf("invalid mode %q", mode)
		}
		result, err := integration.VCSDiff(r.Context(), requestDirectory(r), mode)
		return result, statusFromGenericError(err), err
	})
}

func vcsDiffRaw() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method))
			return
		}
		result, err := integration.VCSDiffRaw(r.Context(), requestDirectory(r))
		if err != nil {
			writeError(w, statusFromGenericError(err), err)
			return
		}
		w.Header().Set("Content-Type", "text/x-diff; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(result))
	}
}

func vcsApply() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodPost {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		var payload struct {
			Patch string `json:"patch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, http.StatusBadRequest, err
		}
		result, err := integration.VCSApply(r.Context(), requestDirectory(r), payload.Patch)
		return result, statusFromGenericError(err), err
	})
}

func agentsList() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.ListAgents(requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func skillsList() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.ListSkills(requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func commandList() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.ListCommands(requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func formatterStatus() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.FormatterStatuses(requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func lspStatus() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.LSPStatuses(requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}

func projectCurrent() http.HandlerFunc {
	return handleJSON(func(r *http.Request) (any, int, error) {
		if r.Method != http.MethodGet {
			return nil, http.StatusMethodNotAllowed, fmt.Errorf("method %s not allowed", r.Method)
		}
		result, err := integration.CurrentProject(r.Context(), requestDirectory(r))
		return result, statusFromGenericError(err), err
	})
}
