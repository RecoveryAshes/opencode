package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

const maxWebFetchBytes = 5 * 1024 * 1024

func webFetchTool(ctx context.Context, request Request) (Result, error) {
	target, err := requireString(request.Params, "url")
	if err != nil {
		return Result{}, err
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Result{}, fmt.Errorf("URL must start with http:// or https://")
	}
	format := optionalString(request.Params, "format", "markdown")
	timeoutSeconds := optionalInt(request.Params, "timeout", 30)
	if timeoutSeconds <= 0 || timeoutSeconds > 120 {
		timeoutSeconds = 120
	}
	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	httpRequest, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, target, nil)
	if err != nil {
		return Result{}, fmt.Errorf("create webfetch request: %w", err)
	}
	httpRequest.Header.Set("User-Agent", "opencode-go")
	httpRequest.Header.Set("Accept-Language", "en-US,en;q=0.9")
	httpRequest.Header.Set("Accept", acceptHeader(format))

	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return Result{}, fmt.Errorf("webfetch request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, fmt.Errorf("webfetch status %d", response.StatusCode)
	}
	reader := io.LimitReader(response.Body, maxWebFetchBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return Result{}, fmt.Errorf("read webfetch response: %w", err)
	}
	if len(data) > maxWebFetchBytes {
		return Result{}, fmt.Errorf("response too large (exceeds 5MB limit)")
	}
	contentType := response.Header.Get("Content-Type")
	content := string(data)
	switch format {
	case "html":
	case "text":
		if strings.Contains(contentType, "text/html") {
			content = htmlToText(content)
		}
	default:
		if strings.Contains(contentType, "text/html") {
			content = htmlToMarkdown(content)
		}
	}
	return Result{
		Title: target + " (" + contentType + ")",
		Metadata: map[string]any{
			"contentType": contentType,
			"url":         target,
			"format":      format,
		},
		Output: content,
	}, nil
}

func webSearchTool(ctx context.Context, request Request) (Result, error) {
	query, err := requireString(request.Params, "query")
	if err != nil {
		return Result{}, err
	}
	if endpoint := strings.TrimSpace(firstNonEmptyEnv("OPENCODE_WEBSEARCH_ENDPOINT")); endpoint != "" {
		return remoteWebSearch(ctx, endpoint, query, request)
	}
	output := "Web search provider is not configured in Go runtime. Set OPENCODE_WEBSEARCH_ENDPOINT to a trusted search bridge."
	return Result{
		Title: "Web Search: " + query,
		Metadata: map[string]any{
			"provider": "unconfigured",
			"query":    query,
		},
		Output: output,
	}, nil
}

func remoteWebSearch(ctx context.Context, endpoint string, query string, request Request) (Result, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Result{}, fmt.Errorf("OPENCODE_WEBSEARCH_ENDPOINT must be http(s)")
	}
	values := parsed.Query()
	values.Set("q", query)
	if numResults := optionalInt(request.Params, "numResults", 0); numResults > 0 {
		values.Set("numResults", fmt.Sprint(numResults))
	}
	parsed.RawQuery = values.Encode()
	webRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Result{}, fmt.Errorf("create websearch request: %w", err)
	}
	webRequest.Header.Set("User-Agent", "opencode-go")
	response, err := http.DefaultClient.Do(webRequest)
	if err != nil {
		return Result{}, fmt.Errorf("websearch request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxWebFetchBytes+1))
	if err != nil {
		return Result{}, fmt.Errorf("read websearch response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, fmt.Errorf("websearch status %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	return Result{
		Title: "Web Search: " + query,
		Metadata: map[string]any{
			"provider": "endpoint",
			"query":    query,
			"endpoint": endpoint,
		},
		Output: string(data),
	}, nil
}

func acceptHeader(format string) string {
	switch format {
	case "html":
		return "text/html,application/xhtml+xml;q=0.9,text/plain;q=0.8,*/*;q=0.1"
	case "text":
		return "text/plain,text/html;q=0.8,*/*;q=0.1"
	default:
		return "text/markdown,text/plain;q=0.8,text/html;q=0.7,*/*;q=0.1"
	}
}

func htmlToText(input string) string {
	clean := input
	for _, tag := range []string{"script", "style", "noscript", "iframe", "object", "embed"} {
		re := regexp.MustCompile(`(?is)<` + tag + `[^>]*>.*?</` + tag + `>`)
		clean = re.ReplaceAllString(clean, "")
	}
	clean = regexp.MustCompile(`(?is)<br\s*/?>`).ReplaceAllString(clean, "\n")
	clean = regexp.MustCompile(`(?is)</p\s*>|</div\s*>|</h[1-6]\s*>`).ReplaceAllString(clean, "\n")
	clean = regexp.MustCompile(`(?is)<[^>]+>`).ReplaceAllString(clean, "")
	clean = htmlEntityReplacer(clean)
	lines := []string{}
	for _, line := range strings.Split(clean, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

func htmlToMarkdown(input string) string {
	text := regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`).ReplaceAllString(input, "# $1\n\n")
	text = regexp.MustCompile(`(?is)<h2[^>]*>(.*?)</h2>`).ReplaceAllString(text, "## $1\n\n")
	text = regexp.MustCompile(`(?is)<h3[^>]*>(.*?)</h3>`).ReplaceAllString(text, "### $1\n\n")
	text = regexp.MustCompile(`(?is)<li[^>]*>(.*?)</li>`).ReplaceAllString(text, "- $1\n")
	text = regexp.MustCompile(`(?is)<a[^>]*href=["']([^"']+)["'][^>]*>(.*?)</a>`).ReplaceAllString(text, "[$2]($1)")
	return htmlToText(text)
}

func htmlEntityReplacer(input string) string {
	replacer := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ")
	return replacer.Replace(input)
}

func firstNonEmptyEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}
