package session

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewIDUsesLegacyPrefix(t *testing.T) {
	id, err := NewID()
	if err != nil {
		t.Fatalf("NewID() error = %v", err)
	}
	if _, err := ParseID(string(id)); err != nil {
		t.Fatalf("ParseID(%q) error = %v", id, err)
	}
}

func TestParseIDRejectsInvalidPrefix(t *testing.T) {
	if _, err := ParseID("not-a-session"); err == nil {
		t.Fatal("ParseID() error = nil, want error")
	}
}

func TestMessageSummaryJSONUnion(t *testing.T) {
	assistant, err := json.Marshal(MessageInfo{Summary: &MessageSummary{Assistant: true}})
	if err != nil {
		t.Fatalf("Marshal(assistant summary) error = %v", err)
	}
	if !strings.Contains(string(assistant), `"summary":true`) {
		t.Fatalf("assistant summary JSON = %s, want boolean true", assistant)
	}

	user, err := json.Marshal(MessageInfo{Summary: &MessageSummary{Title: "title", Body: "body", Diffs: []map[string]any{{"file": "main.go"}}}})
	if err != nil {
		t.Fatalf("Marshal(user summary) error = %v", err)
	}
	if !strings.Contains(string(user), `"summary":{"title":"title","body":"body","diffs":[{"file":"main.go"}]}`) {
		t.Fatalf("user summary JSON = %s, want object summary", user)
	}

	var decodedAssistant MessageInfo
	if err := json.Unmarshal([]byte(`{"summary":true}`), &decodedAssistant); err != nil {
		t.Fatalf("Unmarshal(assistant summary) error = %v", err)
	}
	if decodedAssistant.Summary == nil || !decodedAssistant.Summary.Assistant {
		t.Fatalf("decoded assistant summary = %#v, want assistant marker", decodedAssistant.Summary)
	}

	var decodedUser MessageInfo
	if err := json.Unmarshal([]byte(`{"summary":{"title":"title","body":"body","diffs":[{"file":"main.go"}]}}`), &decodedUser); err != nil {
		t.Fatalf("Unmarshal(user summary) error = %v", err)
	}
	if decodedUser.Summary == nil || decodedUser.Summary.Assistant || decodedUser.Summary.Title != "title" || len(decodedUser.Summary.Diffs) != 1 {
		t.Fatalf("decoded user summary = %#v, want object summary", decodedUser.Summary)
	}
}
