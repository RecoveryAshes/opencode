package llm

import (
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/RecoveryAshes/opencode/internal/config"
)

const (
	modelsDevDefaultSource = "https://models.dev"
	modelsDevCacheTTL      = 5 * time.Minute
	modelsDevHTTPTimeout   = 10 * time.Second
)

//go:embed models_snapshot.json
var modelsDevSnapshot []byte

var modelsDevHTTPClient = &http.Client{Timeout: modelsDevHTTPTimeout}

type modelsDevCatalog struct {
	mu       sync.Mutex
	cacheKey string
	raw      map[string]any
}

var defaultModelsDevCatalog modelsDevCatalog

func modelsDevProviders() map[string]PublicProvider {
	raw := defaultModelsDevCatalog.get()
	if len(raw) == 0 {
		return map[string]PublicProvider{}
	}
	return modelsDevProvidersFromRaw(raw)
}

func refreshModelsDevCatalog(ctx context.Context, force bool) error {
	_, err := defaultModelsDevCatalog.refresh(ctx, force)
	return err
}

func (catalog *modelsDevCatalog) get() map[string]any {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()

	key := modelsDevCacheKey()
	if catalog.raw != nil && catalog.cacheKey == key {
		return cloneModelsDevRaw(catalog.raw)
	}
	raw := loadModelsDevCatalog(context.Background())
	catalog.cacheKey = key
	catalog.raw = raw
	return cloneModelsDevRaw(raw)
}

func (catalog *modelsDevCatalog) refresh(ctx context.Context, force bool) (map[string]any, error) {
	catalog.mu.Lock()
	defer catalog.mu.Unlock()

	if source := strings.TrimSpace(os.Getenv("OPENCODE_MODELS_PATH")); source != "" {
		raw, err := readModelsDevFile(source)
		if err != nil {
			raw = map[string]any{}
		}
		catalog.cacheKey = modelsDevCacheKey()
		catalog.raw = raw
		return cloneModelsDevRaw(raw), nil
	}

	if modelsDevFetchDisabled() {
		raw := loadModelsDevCatalog(ctx)
		catalog.cacheKey = modelsDevCacheKey()
		catalog.raw = raw
		return cloneModelsDevRaw(raw), nil
	}

	cacheFile := modelsDevCacheFile()
	if !force && modelsDevCacheFresh(cacheFile, time.Now()) {
		raw, err := readModelsDevFile(cacheFile)
		if err == nil {
			catalog.cacheKey = modelsDevCacheKey()
			catalog.raw = raw
			return cloneModelsDevRaw(raw), nil
		}
	}

	raw, err := fetchAndCacheModelsDev(ctx, cacheFile)
	if err != nil {
		fallback := loadModelsDevCatalog(ctx)
		catalog.cacheKey = modelsDevCacheKey()
		catalog.raw = fallback
		return cloneModelsDevRaw(fallback), err
	}
	catalog.cacheKey = modelsDevCacheKey()
	catalog.raw = raw
	return cloneModelsDevRaw(raw), nil
}

func loadModelsDevCatalog(ctx context.Context) map[string]any {
	if source := strings.TrimSpace(os.Getenv("OPENCODE_MODELS_PATH")); source != "" {
		if raw, err := readModelsDevFile(source); err == nil {
			return raw
		}
		return map[string]any{}
	}
	cacheFile := modelsDevCacheFile()
	if raw, err := readModelsDevFile(cacheFile); err == nil {
		return raw
	}
	if raw, err := readModelsDevJSON(modelsDevSnapshot); err == nil {
		return raw
	}
	if modelsDevFetchDisabled() {
		return map[string]any{}
	}
	if raw, err := fetchAndCacheModelsDev(ctx, cacheFile); err == nil {
		return raw
	}
	return map[string]any{}
}

func modelsDevProvidersFromRaw(raw map[string]any) map[string]PublicProvider {
	result := map[string]PublicProvider{}
	for id, value := range raw {
		record, ok := value.(map[string]any)
		if !ok {
			continue
		}
		provider := providerFromModelsDev(id, record)
		if provider.ID == "" {
			continue
		}
		result[provider.ID] = provider
	}
	return result
}

func readModelsDevFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return readModelsDevJSON(data)
}

func readModelsDevJSON(data []byte) (map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func fetchAndCacheModelsDev(ctx context.Context, cacheFile string) (map[string]any, error) {
	source := strings.TrimRight(modelsDevSource(), "/")
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source+"/api.json", nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", modelsDevUserAgent())

	response, err := modelsDevHTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil, fmt.Errorf("fetch models.dev catalog: %s", response.Status)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	raw, err := readModelsDevJSON(data)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(cacheFile), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(cacheFile, data, 0o644); err != nil {
		return nil, err
	}
	return raw, nil
}

func modelsDevCacheFile() string {
	source := modelsDevSource()
	name := "models.json"
	if source != modelsDevDefaultSource {
		name = "models-" + sha1Hex(source) + ".json"
	}
	return filepath.Join(config.GlobalCacheDir(), name)
}

func modelsDevCacheFresh(path string, now time.Time) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return now.Sub(info.ModTime()) < modelsDevCacheTTL
}

func modelsDevCacheKey() string {
	if path := strings.TrimSpace(os.Getenv("OPENCODE_MODELS_PATH")); path != "" {
		return "path:" + path
	}
	return "source:" + modelsDevSource() + ":disabled:" + os.Getenv("OPENCODE_DISABLE_MODELS_FETCH")
}

func modelsDevSource() string {
	source := strings.TrimSpace(os.Getenv("OPENCODE_MODELS_URL"))
	if source == "" {
		return modelsDevDefaultSource
	}
	return source
}

func modelsDevFetchDisabled() bool {
	switch strings.ToLower(os.Getenv("OPENCODE_DISABLE_MODELS_FETCH")) {
	case "1", "true":
		return true
	default:
		return false
	}
}

func modelsDevUserAgent() string {
	client := strings.TrimSpace(os.Getenv("OPENCODE_CLIENT"))
	if client == "" {
		client = "cli"
	}
	return "opencode/go/dev/" + client
}

func sha1Hex(input string) string {
	sum := sha1.Sum([]byte(input))
	return hex.EncodeToString(sum[:])
}

func cloneModelsDevRaw(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = cloneModelsDevValue(value)
	}
	return result
}

func cloneModelsDevValue(input any) any {
	switch value := input.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, nested := range value {
			result[key] = cloneModelsDevValue(nested)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, nested := range value {
			result[i] = cloneModelsDevValue(nested)
		}
		return result
	default:
		return input
	}
}
