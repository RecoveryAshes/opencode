package retry

import (
	"reflect"
	"testing"
	"time"
)

func TestDelayWithoutHeadersCapsAtThirtySeconds(t *testing.T) {
	got := make([]float64, 0, 10)
	for attempt := 1; attempt <= 10; attempt++ {
		got = append(got, Delay(attempt, &APIError{IsRetryable: true, Message: "boom"}, fixedClock()))
	}
	want := []float64{2000, 4000, 8000, 16000, 30000, 30000, 30000, 30000, 30000, 30000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Delay sequence = %#v, want %#v", got, want)
	}
}

func TestDelayHeaders(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		headers map[string]string
		want    float64
	}{
		{
			name:    "prefers retry-after-ms",
			attempt: 4,
			headers: map[string]string{"retry-after-ms": "1500"},
			want:    1500,
		},
		{
			name:    "matches JavaScript parseFloat prefix behavior",
			attempt: 1,
			headers: map[string]string{"retry-after-ms": "1500ms"},
			want:    1500,
		},
		{
			name:    "uses retry-after seconds",
			attempt: 3,
			headers: map[string]string{"retry-after": "30"},
			want:    30000,
		},
		{
			name:    "ignores invalid retry-after",
			attempt: 1,
			headers: map[string]string{"retry-after": "not-a-number"},
			want:    2000,
		},
		{
			name:    "uses long retry-after values with headers",
			attempt: 1,
			headers: map[string]string{"retry-after": "50"},
			want:    50000,
		},
		{
			name:    "caps oversized header delay",
			attempt: 1,
			headers: map[string]string{"retry-after-ms": "999999999999"},
			want:    MaxDelay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Delay(tt.attempt, &APIError{IsRetryable: true, Message: "boom", ResponseHeaders: tt.headers}, fixedClock())
			if got != tt.want {
				t.Fatalf("Delay() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDelayHTTPDate(t *testing.T) {
	now := time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	future := now.Add(20 * time.Second).UTC().Format(time.RFC1123)
	got := Delay(1, &APIError{
		IsRetryable:     true,
		Message:         "boom",
		ResponseHeaders: map[string]string{"retry-after": future},
	}, func() time.Time { return now })
	if got != 20000 {
		t.Fatalf("Delay() = %v, want 20000", got)
	}
}

func TestRetryablePlainAndJSONErrors(t *testing.T) {
	tests := []struct {
		name string
		err  NamedError
		want *Retryable
	}{
		{
			name: "maps too_many_requests json messages",
			err:  wrappedMessage(`{"type":"error","error":{"type":"too_many_requests"}}`),
			want: &Retryable{Message: "Too Many Requests"},
		},
		{
			name: "maps overloaded provider codes",
			err:  wrappedMessage(`{"code":"resource_exhausted"}`),
			want: &Retryable{Message: "Provider is overloaded"},
		},
		{
			name: "does not retry unknown json",
			err:  wrappedMessage(`{"error":{"message":"no_kv_space"}}`),
			want: nil,
		},
		{
			name: "does not throw on numeric error codes",
			err:  wrappedMessage(`{"type":"error","error":{"code":123}}`),
			want: nil,
		},
		{
			name: "returns nil for non-json message",
			err:  wrappedMessage("not-json"),
			want: nil,
		},
		{
			name: "retries plain text rate limit errors",
			err:  wrappedMessage("Rate limit exceeded, please try again later"),
			want: &Retryable{Message: "Rate limit exceeded, please try again later"},
		},
		{
			name: "retries too many requests in plain text",
			err:  wrappedMessage("Too many requests, please slow down"),
			want: &Retryable{Message: "Too many requests, please slow down"},
		},
		{
			name: "does not retry context overflow",
			err:  ContextOverflowNamed("Input exceeds context window of this model"),
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetryableError(tt.err, "test")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RetryableError() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRetryableAPIErrors(t *testing.T) {
	status400 := 400
	status500 := 500
	status502 := 502
	status503 := 503
	tests := []struct {
		name string
		err  APIError
		want *Retryable
	}{
		{
			name: "retries 500 even when not explicitly retryable",
			err:  APIError{Message: "Internal server error", IsRetryable: false, StatusCode: &status500},
			want: &Retryable{Message: "Internal server error"},
		},
		{
			name: "retries 502",
			err:  APIError{Message: "Bad gateway", IsRetryable: false, StatusCode: &status502},
			want: &Retryable{Message: "Bad gateway"},
		},
		{
			name: "retries 503",
			err:  APIError{Message: "Service unavailable", IsRetryable: false, StatusCode: &status503},
			want: &Retryable{Message: "Service unavailable"},
		},
		{
			name: "does not retry non-retryable 4xx",
			err:  APIError{Message: "Bad request", IsRetryable: false, StatusCode: &status400},
			want: nil,
		},
		{
			name: "retries decompression failure",
			err:  APIError{Message: "Response decompression failed", IsRetryable: true, Metadata: map[string]string{"code": "ZlibError"}},
			want: &Retryable{Message: "Response decompression failed"},
		},
		{
			name: "maps overloaded messages",
			err:  APIError{Message: "Provider Overloaded", IsRetryable: true},
			want: &Retryable{Message: "Provider is overloaded"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetryableError(APIErrorNamed(tt.err), "test")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RetryableError() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRetryableFreeLimit(t *testing.T) {
	got := RetryableError(APIErrorNamed(APIError{
		Message:      "Free usage exceeded",
		IsRetryable:  true,
		ResponseBody: `{"type":"error","error":{"type":"FreeUsageLimitError","message":"Free usage exceeded"}}`,
	}), "opencode")

	want := &Retryable{
		Message: GoUpsellMessage,
		Action: &Action{
			Reason:   ReasonFreeTierLimit,
			Provider: "opencode",
			Title:    "Free limit reached",
			Message:  "Subscribe to OpenCode Go for reliable access to the best open-source models, starting at $5/month.",
			Label:    "subscribe",
			Link:     GoUpsellURL,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RetryableError() = %#v, want %#v", got, want)
	}
}

func TestRetryableGoLimit(t *testing.T) {
	got := RetryableError(APIErrorNamed(APIError{
		Message:         "Subscription quota exceeded. You can continue using free models.",
		IsRetryable:     true,
		ResponseHeaders: map[string]string{"retry-after": "19380"},
		ResponseBody: `{
			"type":"error",
			"error":{"type":"GoUsageLimitError","message":"Subscription quota exceeded. You can continue using free models."},
			"metadata":{"workspace":"wrk_01K6XGM22R6FM8JVABE9XDQXGH","limitName":"5 hour"}
		}`,
	}), "opencode-go")

	wantMessage := "5 hour usage limit reached. It will reset in 5 hours 23 minutes. To continue using this model now, enable usage from your available balance"
	want := &Retryable{
		Message: wantMessage + " - https://opencode.ai/workspace/wrk_01K6XGM22R6FM8JVABE9XDQXGH/go",
		Action: &Action{
			Reason:   ReasonAccountLimit,
			Provider: "opencode-go",
			Title:    "Go limit reached",
			Message:  wantMessage,
			Label:    "open settings",
			Link:     "https://opencode.ai/workspace/wrk_01K6XGM22R6FM8JVABE9XDQXGH/go",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RetryableError() = %#v, want %#v", got, want)
	}
}

func TestRetryableGoLimitWithoutLimitMetadata(t *testing.T) {
	got := RetryableError(APIErrorNamed(APIError{
		Message:         "Subscription quota exceeded. You can continue using free models.",
		IsRetryable:     true,
		ResponseHeaders: map[string]string{"retry-after": "900"},
		ResponseBody: `{
			"type":"error",
			"error":{"type":"GoUsageLimitError","message":"Subscription quota exceeded. You can continue using free models."},
			"metadata":{"workspace":"wrk_01K6XGM22R6FM8JVABE9XDQXGH"}
		}`,
	}), "opencode-go")

	if got == nil || got.Action == nil {
		t.Fatalf("RetryableError() = %#v, want action", got)
	}
	want := "Usage limit reached. It will reset in 15 minutes. To continue using this model now, enable usage from your available balance"
	if got.Action.Message != want {
		t.Fatalf("action message = %q, want %q", got.Action.Message, want)
	}
}

func fixedClock() Clock {
	return func() time.Time {
		return time.Date(2026, 5, 17, 10, 0, 0, 0, time.UTC)
	}
}

func wrappedMessage(message any) NamedError {
	return NamedError{Name: "", Data: map[string]any{"message": message}}
}
