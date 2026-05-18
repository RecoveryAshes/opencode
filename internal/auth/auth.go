// Package auth persists provider credentials across the legacy provider auth
// file and the AuthV2 account file.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OAuthDummyKey is the placeholder API key used by OAuth-only providers in the
// TypeScript runtime.
const OAuthDummyKey = "opencode-oauth-dummy-key"

// Credential is a persisted provider credential.
type Credential struct {
	Type     string            `json:"type"`
	Key      string            `json:"key,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Refresh  string            `json:"refresh,omitempty"`
	Access   string            `json:"access,omitempty"`
	Expires  int64             `json:"expires,omitempty"`
}

// Account is an AuthV2 account record.
type Account struct {
	ID          string     `json:"id"`
	ServiceID   string     `json:"serviceID"`
	Description string     `json:"description"`
	Credential  Credential `json:"credential"`
}

// Store reads and writes provider credential files.
type Store struct {
	DataDir string
}

type writable struct {
	Version  int                `json:"version"`
	Accounts map[string]Account `json:"accounts"`
	Active   map[string]string  `json:"active"`
}

// DefaultStore returns a store rooted at the opencode XDG data directory.
func DefaultStore() Store {
	return Store{DataDir: DataDir()}
}

// DataDir returns the opencode XDG data directory used by the TypeScript
// Global.Path.data implementation.
func DataDir() string {
	if base := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); base != "" {
		return filepath.Join(base, "opencode")
	}
	home := strings.TrimSpace(os.Getenv("OPENCODE_TEST_HOME"))
	if home == "" {
		home = strings.TrimSpace(os.Getenv("HOME"))
	}
	if home == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	if home == "" {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "opencode")
}

// Active returns the active account for a service, falling back to the first
// account for that service to match AuthV2.active.
func (store Store) Active(serviceID string) (*Account, error) {
	data, err := store.load()
	if err != nil {
		return nil, err
	}
	if id := data.Active[serviceID]; id != "" {
		if account, ok := data.Accounts[id]; ok {
			return &account, nil
		}
	}
	for _, account := range data.Accounts {
		if account.ServiceID == serviceID {
			return &account, nil
		}
	}
	return nil, nil
}

// SetActive creates or updates the active account for a service.
func (store Store) SetActive(serviceID string, credential Credential, description string) (Account, error) {
	if strings.TrimSpace(serviceID) == "" {
		return Account{}, fmt.Errorf("serviceID is required")
	}
	if description == "" {
		description = "default"
	}
	data, err := store.load()
	if err != nil {
		return Account{}, err
	}
	id := data.Active[serviceID]
	if id == "" {
		for _, account := range data.Accounts {
			if account.ServiceID == serviceID {
				id = account.ID
				break
			}
		}
	}
	if id == "" {
		id, err = newAccountID()
		if err != nil {
			return Account{}, err
		}
	}
	account := Account{
		ID:          id,
		ServiceID:   serviceID,
		Description: description,
		Credential:  credential,
	}
	data.Accounts[id] = account
	data.Active[serviceID] = id
	if err := store.write(data); err != nil {
		return Account{}, err
	}
	return account, nil
}

func (store Store) load() (writable, error) {
	if content := strings.TrimSpace(os.Getenv("OPENCODE_AUTH_CONTENT")); content != "" {
		data, migrated, err := decodeAuthContent([]byte(content))
		if err != nil {
			return writable{Version: 2, Accounts: map[string]Account{}, Active: map[string]string{}}, nil
		}
		if migrated {
			if err := store.write(data); err != nil {
				return writable{}, fmt.Errorf("migrate auth content: %w", err)
			}
		}
		return data, nil
	}

	if raw, err := os.ReadFile(store.file()); err == nil {
		if strings.TrimSpace(string(raw)) == "" {
			return emptyWritable(), nil
		}
		data, err := decodeWritable(raw)
		if err != nil {
			legacy := map[string]Credential{}
			if unmarshalCredentialMap(raw, legacy) == nil {
				data, err = migrateV1(legacy)
				if err != nil {
					return writable{}, err
				}
				if err := store.write(data); err != nil {
					return writable{}, fmt.Errorf("migrate auth file: %w", err)
				}
				return data, nil
			}
			return writable{}, fmt.Errorf("decode auth file %s: %w", store.file(), err)
		}
		return data, nil
	} else if !os.IsNotExist(err) {
		return writable{}, fmt.Errorf("read auth file %s: %w", store.file(), err)
	}

	if legacy, err := readCredentialMap(store.legacyFile()); err == nil && len(legacy) > 0 {
		data, err := migrateV1(legacy)
		if err != nil {
			return writable{}, err
		}
		if err := store.writeV2(data); err != nil {
			return writable{}, fmt.Errorf("migrate auth file: %w", err)
		}
		return data, nil
	}

	return emptyWritable(), nil
}

func (store Store) write(data writable) error {
	if err := store.writeV2(data); err != nil {
		return err
	}
	if err := store.writeLegacy(data); err != nil {
		return err
	}
	return nil
}

func (store Store) writeV2(data writable) error {
	if data.Version == 0 {
		data.Version = 2
	}
	if data.Accounts == nil {
		data.Accounts = map[string]Account{}
	}
	if data.Active == nil {
		data.Active = map[string]string{}
	}
	file := store.file()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return fmt.Errorf("create auth directory %s: %w", filepath.Dir(file), err)
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("encode auth file %s: %w", file, err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(file, encoded, 0o600); err != nil {
		return fmt.Errorf("write auth file %s: %w", file, err)
	}
	return nil
}

func (store Store) writeLegacy(data writable) error {
	credentials := map[string]Credential{}
	for serviceID, accountID := range data.Active {
		account, ok := data.Accounts[accountID]
		if ok {
			credentials[serviceID] = account.Credential
		}
	}
	for _, account := range data.Accounts {
		if _, ok := credentials[account.ServiceID]; ok {
			continue
		}
		credentials[account.ServiceID] = account.Credential
	}
	file := store.legacyFile()
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return fmt.Errorf("create auth directory %s: %w", filepath.Dir(file), err)
	}
	encoded, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		return fmt.Errorf("encode legacy auth file %s: %w", file, err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(file, encoded, 0o600); err != nil {
		return fmt.Errorf("write legacy auth file %s: %w", file, err)
	}
	return nil
}

func (store Store) file() string {
	if store.DataDir == "" {
		store.DataDir = DataDir()
	}
	return filepath.Join(store.DataDir, "auth-v2.json")
}

func (store Store) legacyFile() string {
	if store.DataDir == "" {
		store.DataDir = DataDir()
	}
	return filepath.Join(store.DataDir, "auth.json")
}

func emptyWritable() writable {
	return writable{
		Version:  2,
		Accounts: map[string]Account{},
		Active:   map[string]string{},
	}
}

func decodeWritable(raw []byte) (writable, error) {
	var data writable
	if err := json.Unmarshal(raw, &data); err != nil {
		return writable{}, err
	}
	if data.Version != 2 {
		return writable{}, fmt.Errorf("unsupported auth version %d", data.Version)
	}
	if data.Accounts == nil {
		data.Accounts = map[string]Account{}
	}
	if data.Active == nil {
		data.Active = map[string]string{}
	}
	return data, nil
}

func decodeAuthContent(raw []byte) (writable, bool, error) {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return writable{}, false, err
	}
	if version, ok := probe["version"].(float64); ok && int(version) == 2 {
		data, err := decodeWritable(raw)
		return data, false, err
	}
	legacy := map[string]Credential{}
	if err := unmarshalCredentialMap(raw, legacy); err != nil {
		return writable{}, false, err
	}
	data, err := migrateV1(legacy)
	return data, true, err
}

func readCredentialMap(file string) (map[string]Credential, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	legacy := map[string]Credential{}
	if err := unmarshalCredentialMap(raw, legacy); err != nil {
		return nil, err
	}
	return legacy, nil
}

func unmarshalCredentialMap(raw []byte, out map[string]Credential) error {
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return err
	}
	for serviceID, value := range decoded {
		var credential Credential
		if err := json.Unmarshal(value, &credential); err != nil {
			continue
		}
		if credential.Type != "api" && credential.Type != "oauth" {
			continue
		}
		out[serviceID] = credential
	}
	return nil
}

func migrateV1(legacy map[string]Credential) (writable, error) {
	data := emptyWritable()
	for serviceID, credential := range legacy {
		id, err := newAccountID()
		if err != nil {
			return writable{}, err
		}
		data.Accounts[id] = Account{
			ID:          id,
			ServiceID:   serviceID,
			Description: "default",
			Credential:  credential,
		}
		data.Active[serviceID] = id
	}
	return data, nil
}

func newAccountID() (string, error) {
	random := make([]byte, 5)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate account id: %w", err)
	}
	return fmt.Sprintf("acc_%x_%s", time.Now().UnixMilli(), hex.EncodeToString(random)), nil
}
