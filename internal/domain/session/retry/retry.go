// Package retry contains the session retry policy rules ported from the
// TypeScript opencode session retry module.
package retry

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// GoUpsellMessage is the user-facing message for free-tier exhaustion.
	GoUpsellMessage = "Free usage exceeded, subscribe to Go"
	// GoUpsellURL is the settings page shown for free-tier exhaustion.
	GoUpsellURL = "https://opencode.ai/go"

	// InitialDelay is the first retry delay in milliseconds.
	InitialDelay = 2_000
	// BackoffFactor is the exponential retry multiplier.
	BackoffFactor = 2
	// MaxDelayNoHeader caps exponential backoff when retry headers are absent.
	MaxDelayNoHeader = 30_000
	// MaxDelay caps header-provided delays at the JavaScript timer limit.
	MaxDelay = 2_147_483_647
)

// Reason identifies a retry action category.
type Reason string

const (
	// ReasonFreeTierLimit indicates the unauthenticated/free-tier limit was hit.
	ReasonFreeTierLimit Reason = "free_tier_limit"
	// ReasonAccountLimit indicates an account or workspace usage limit was hit.
	ReasonAccountLimit Reason = "account_rate_limit"

	contextOverflowError = "ContextOverflowError"
	apiError             = "APIError"
)

// APIError is the Go contract for TypeScript's MessageV2.APIError data shape.
type APIError struct {
	Message         string
	StatusCode      *int
	IsRetryable     bool
	ResponseHeaders map[string]string
	ResponseBody    string
	Metadata        map[string]string
}

// NamedError preserves the serializable NamedError contract used by opencode.
type NamedError struct {
	Name string
	Data map[string]any
}

// Action describes a user-visible remediation for a retryable error.
type Action struct {
	Reason   Reason
	Provider string
	Title    string
	Message  string
	Label    string
	Link     string
}

// Retryable is the user-visible result of classifying a provider error.
type Retryable struct {
	Message string
	Action  *Action
}

// Clock supplies the current time for deterministic retry-after date tests.
type Clock func() time.Time

// Delay returns the retry wait in milliseconds for a one-based attempt number.
func Delay(attempt int, err *APIError, now Clock) float64 {
	if attempt < 1 {
		attempt = 1
	}
	if now == nil {
		now = time.Now
	}

	if err != nil && err.ResponseHeaders != nil {
		if value := err.ResponseHeaders["retry-after-ms"]; value != "" {
			if parsed, ok := parseFloat(value); ok {
				return capDelay(parsed)
			}
		}

		if value := err.ResponseHeaders["retry-after"]; value != "" {
			if parsed, ok := parseFloat(value); ok {
				return capDelay(math.Ceil(parsed * 1000))
			}
			if parsed, ok := parseHTTPTime(value); ok {
				ms := parsed.Sub(now()).Milliseconds()
				if ms > 0 {
					return capDelay(float64(ms))
				}
			}
		}

		return capDelay(float64(InitialDelay) * math.Pow(BackoffFactor, float64(attempt-1)))
	}

	backoff := float64(InitialDelay) * math.Pow(BackoffFactor, float64(attempt-1))
	if backoff > MaxDelayNoHeader {
		backoff = MaxDelayNoHeader
	}
	return capDelay(backoff)
}

// RetryableError maps opencode error objects to user-visible retry metadata.
func RetryableError(err NamedError, provider string) *Retryable {
	if err.Name == contextOverflowError {
		return nil
	}

	if err.Name == apiError {
		return retryableAPIError(apiErrorFromData(err.Data), provider)
	}

	msg := messageFromData(err.Data)
	if msg == "" {
		return nil
	}
	lower := strings.ToLower(msg)
	if strings.Contains(lower, "rate increased too quickly") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests") {
		return &Retryable{Message: msg}
	}

	body := parseJSONObject(msg)
	if body == nil {
		return nil
	}

	code := stringValue(body["code"])
	errorBody := objectValue(body["error"])
	if stringValue(body["type"]) == "error" && stringValue(errorBody["type"]) == "too_many_requests" {
		return &Retryable{Message: "Too Many Requests"}
	}
	if strings.Contains(code, "exhausted") || strings.Contains(code, "unavailable") {
		return &Retryable{Message: "Provider is overloaded"}
	}
	if stringValue(body["type"]) == "error" && strings.Contains(stringValue(errorBody["code"]), "rate_limit") {
		return &Retryable{Message: "Rate Limited"}
	}
	return nil
}

// APIErrorNamed wraps an APIError in opencode's serializable NamedError shape.
func APIErrorNamed(input APIError) NamedError {
	data := map[string]any{
		"message":     input.Message,
		"isRetryable": input.IsRetryable,
	}
	if input.StatusCode != nil {
		data["statusCode"] = *input.StatusCode
	}
	if input.ResponseHeaders != nil {
		data["responseHeaders"] = input.ResponseHeaders
	}
	if input.ResponseBody != "" {
		data["responseBody"] = input.ResponseBody
	}
	if input.Metadata != nil {
		data["metadata"] = input.Metadata
	}
	return NamedError{Name: apiError, Data: data}
}

// ContextOverflowNamed wraps a context overflow message in the NamedError shape.
func ContextOverflowNamed(message string) NamedError {
	return NamedError{
		Name: contextOverflowError,
		Data: map[string]any{"message": message},
	}
}

func retryableAPIError(api APIError, provider string) *Retryable {
	statusRetryable := api.StatusCode != nil && *api.StatusCode >= 500
	if !api.IsRetryable && !statusRetryable {
		return nil
	}
	if strings.Contains(api.ResponseBody, "FreeUsageLimitError") {
		return &Retryable{
			Message: GoUpsellMessage,
			Action: &Action{
				Reason:   ReasonFreeTierLimit,
				Provider: provider,
				Title:    "Free limit reached",
				Message:  "Subscribe to OpenCode Go for reliable access to the best open-source models, starting at $5/month.",
				Label:    "subscribe",
				Link:     GoUpsellURL,
			},
		}
	}
	if strings.Contains(api.ResponseBody, "GoUsageLimitError") {
		return retryableGoUsageLimit(api, provider)
	}
	if strings.Contains(api.Message, "Overloaded") {
		return &Retryable{Message: "Provider is overloaded"}
	}
	return &Retryable{Message: api.Message}
}

func retryableGoUsageLimit(api APIError, provider string) *Retryable {
	body := parseJSONObject(api.ResponseBody)
	metadata := objectValue(body["metadata"])
	workspace := stringValue(metadata["workspace"])
	limitName := stringValue(metadata["limitName"])
	retryAfter, hasRetryAfter := parseFloat(api.ResponseHeaders["retry-after"])
	resetIn := ""
	if hasRetryAfter {
		resetIn = formatResetIn(retryAfter)
	}
	limit := "Usage limit"
	if limitName != "" {
		limit = limitName + " usage limit"
	}
	message := limit + " reached. It will reset in " + resetIn + ". To continue using this model now, enable usage from your available balance"
	link := "https://opencode.ai/workspace/" + workspace + "/go"
	return &Retryable{
		Message: message + " - " + link,
		Action: &Action{
			Reason:   ReasonAccountLimit,
			Provider: provider,
			Title:    "Go limit reached",
			Message:  message,
			Label:    "open settings",
			Link:     link,
		},
	}
}

func capDelay(ms float64) float64 {
	if math.IsNaN(ms) {
		return 0
	}
	if ms > MaxDelay {
		return MaxDelay
	}
	return ms
}

func parseFloat(value string) (float64, bool) {
	value = strings.TrimLeftFunc(value, unicode.IsSpace)
	if value == "" {
		return 0, false
	}
	end := parseFloatEnd(value)
	if end == 0 {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value[:end], 64)
	if err != nil || math.IsNaN(parsed) {
		return 0, false
	}
	return parsed, true
}

func parseFloatEnd(value string) int {
	index := 0
	if value[index] == '+' || value[index] == '-' {
		index++
	}
	if strings.HasPrefix(value[index:], "Infinity") {
		return index + len("Infinity")
	}

	startDigits := index
	for index < len(value) && isASCIIDigit(value[index]) {
		index++
	}
	hasDigits := index > startDigits
	if index < len(value) && value[index] == '.' {
		index++
		fractionStart := index
		for index < len(value) && isASCIIDigit(value[index]) {
			index++
		}
		hasDigits = hasDigits || index > fractionStart
	}
	if !hasDigits {
		return 0
	}

	if index < len(value) && (value[index] == 'e' || value[index] == 'E') {
		expStart := index
		index++
		if index < len(value) && (value[index] == '+' || value[index] == '-') {
			index++
		}
		digitStart := index
		for index < len(value) && isASCIIDigit(value[index]) {
			index++
		}
		if index == digitStart {
			return expStart
		}
	}
	return index
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func parseHTTPTime(value string) (time.Time, bool) {
	formats := []string{
		time.RFC1123,
		time.RFC1123Z,
		time.RFC850,
		time.ANSIC,
	}
	for _, format := range formats {
		parsed, err := time.Parse(format, value)
		if err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func apiErrorFromData(data map[string]any) APIError {
	api := APIError{
		Message:         stringValue(data["message"]),
		IsRetryable:     boolValue(data["isRetryable"]),
		ResponseBody:    stringValue(data["responseBody"]),
		ResponseHeaders: stringMap(data["responseHeaders"]),
		Metadata:        stringMap(data["metadata"]),
	}
	if status, ok := intValue(data["statusCode"]); ok {
		api.StatusCode = &status
	}
	return api
}

func messageFromData(data map[string]any) string {
	if msg := stringValue(data["message"]); msg != "" {
		return msg
	}
	nested := objectValue(data["data"])
	return stringValue(nested["message"])
}

func parseJSONObject(value string) map[string]any {
	var result map[string]any
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return nil
	}
	return result
}

func formatResetIn(secondsFloat float64) string {
	seconds := int(math.Max(0, math.Ceil(secondsFloat)))
	days := seconds / 86_400
	hours := (seconds % 86_400) / 3_600
	minutes := int(math.Ceil(float64(seconds%3_600) / 60))

	if days > 0 {
		if hours > 0 {
			return unit(days, "day") + " " + unit(hours, "hour")
		}
		return unit(days, "day")
	}
	if hours > 0 {
		if minutes > 0 {
			return unit(hours, "hour") + " " + unit(minutes, "minute")
		}
		return unit(hours, "hour")
	}
	if minutes > 0 {
		return unit(minutes, "minute")
	}
	return "less than a minute"
}

func unit(value int, name string) string {
	suffix := ""
	if value != 1 {
		suffix = "s"
	}
	return strconv.Itoa(value) + " " + name + suffix
}

func objectValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(toString(typed)), "\x00", ""))
	}
}

func toString(value any) string {
	switch typed := value.(type) {
	case json.Number:
		return typed.String()
	case float64:
		if typed == math.Trunc(typed) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func stringMap(value any) map[string]string {
	switch typed := value.(type) {
	case map[string]string:
		return typed
	case map[string]any:
		result := make(map[string]string, len(typed))
		for key, item := range typed {
			if str := stringValue(item); str != "" {
				result[key] = str
			}
		}
		return result
	default:
		return nil
	}
}
