package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStoreMigratesLegacyAuthJSON(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "auth.json"), []byte(`{
		"anthropic": {"type":"api","key":"sk-ant","metadata":{"source":"legacy"}},
		"openai": {"type":"oauth","access":"access-token","refresh":"refresh-token","expires":123}
	}`), 0o600); err != nil {
		t.Fatalf("write legacy auth: %v", err)
	}
	store := Store{DataDir: dataDir}

	account, err := store.Active("anthropic")
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	if account == nil || account.Credential.Type != "api" || account.Credential.Key != "sk-ant" || account.Credential.Metadata["source"] != "legacy" {
		t.Fatalf("account = %#v, want migrated anthropic api credential", account)
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, "auth-v2.json"))
	if err != nil {
		t.Fatalf("read auth-v2: %v", err)
	}
	var file struct {
		Version  int                `json:"version"`
		Accounts map[string]Account `json:"accounts"`
		Active   map[string]string  `json:"active"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("decode auth-v2: %v", err)
	}
	if file.Version != 2 || file.Active["anthropic"] == "" || file.Active["openai"] == "" {
		t.Fatalf("auth-v2 = %#v, want active migrated accounts", file)
	}
}

func TestStoreSetActiveCreatesAndUpdatesAuthV2Account(t *testing.T) {
	store := Store{DataDir: t.TempDir()}

	first, err := store.SetActive("local-ai", Credential{Type: "api", Key: "first"}, "default")
	if err != nil {
		t.Fatalf("SetActive(first) error = %v", err)
	}
	second, err := store.SetActive("local-ai", Credential{Type: "api", Key: "second", Metadata: map[string]string{"source": "plugin"}}, "updated")
	if err != nil {
		t.Fatalf("SetActive(second) error = %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second account id = %q, want existing %q", second.ID, first.ID)
	}
	account, err := store.Active("local-ai")
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	if account == nil || account.Description != "updated" || account.Credential.Key != "second" || account.Credential.Metadata["source"] != "plugin" {
		t.Fatalf("account = %#v, want updated active credential", account)
	}
	info, err := os.Stat(filepath.Join(store.DataDir, "auth-v2.json"))
	if err != nil {
		t.Fatalf("stat auth-v2: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("auth-v2 permissions = %o, want 600", info.Mode().Perm())
	}
	legacyRaw, err := os.ReadFile(filepath.Join(store.DataDir, "auth.json"))
	if err != nil {
		t.Fatalf("read legacy auth: %v", err)
	}
	var legacy map[string]Credential
	if err := json.Unmarshal(legacyRaw, &legacy); err != nil {
		t.Fatalf("decode legacy auth: %v", err)
	}
	if legacy["local-ai"].Key != "second" || legacy["local-ai"].Metadata["source"] != "plugin" {
		t.Fatalf("legacy auth = %#v, want mirrored credential", legacy)
	}
}

func TestStoreLoadsAuthContentV1(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"github-copilot":{"type":"api","key":"copilot-key"}}`)
	store := Store{DataDir: dataDir}

	account, err := store.Active("github-copilot")
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	if account == nil || account.Credential.Key != "copilot-key" {
		t.Fatalf("account = %#v, want auth content credential", account)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "auth-v2.json")); err != nil {
		t.Fatalf("auth content should be migrated to auth-v2.json: %v", err)
	}
}

func TestStorePrefersAuthV2OverLegacy(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "auth.json"), []byte(`{"openrouter":{"type":"api","key":"legacy-key"}}`), 0o600); err != nil {
		t.Fatalf("write legacy auth: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "auth-v2.json"), []byte(`{
		"version": 2,
		"accounts": {
			"acc_v2": {
				"id": "acc_v2",
				"serviceID": "openrouter",
				"description": "default",
				"credential": {"type":"api","key":"v2-key"}
			}
		},
		"active": {"openrouter":"acc_v2"}
	}`), 0o600); err != nil {
		t.Fatalf("write auth-v2: %v", err)
	}

	account, err := (Store{DataDir: dataDir}).Active("openrouter")
	if err != nil {
		t.Fatalf("Active() error = %v", err)
	}
	if account == nil || account.Credential.Key != "v2-key" {
		t.Fatalf("account = %#v, want auth-v2 credential", account)
	}
}
