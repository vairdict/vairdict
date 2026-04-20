package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vairdict/vairdict/internal/config"
)

// testResult is a simple struct for testing structured output.
type testResult struct {
	Answer string `json:"answer"`
	Score  int    `json:"score"`
}

func makeMessagesResponse(content string) string {
	resp := messagesResponse{
		Content:    []contentBlock{{Type: "text", Text: content}},
		StopReason: "end_turn",
		Usage:      usage{InputTokens: 10, OutputTokens: 20},
	}
	data, _ := json.Marshal(resp)
	return string(data)
}

func TestNewClient_MissingAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := NewClient(nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T: %v", err, err)
	}
}

func TestNewClient_WithAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	c, err := NewClient(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.model != defaultModel {
		t.Errorf("expected default model %s, got %s", defaultModel, c.model)
	}
}

func TestNewClient_ModelFromConfig(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	cfg := &config.Config{
		Agents: config.AgentsConfig{Model: "claude-opus-4-20250514"},
	}
	c, err := NewClient(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.model != "claude-opus-4-20250514" {
		t.Errorf("expected model from config, got %s", c.model)
	}
}

func TestNewClient_WithOptions(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")
	c, err := NewClient(nil, WithModel("custom-model"), WithEndpoint("http://localhost:9999"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.model != "custom-model" {
		t.Errorf("expected custom-model, got %s", c.model)
	}
	if c.endpoint != "http://localhost:9999" {
		t.Errorf("expected custom endpoint, got %s", c.endpoint)
	}
}

func TestComplete_Success(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify headers.
		if r.Header.Get("x-api-key") != "sk-test-key" {
			t.Errorf("expected api key header, got %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version header, got %q", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected content-type header, got %q", r.Header.Get("Content-Type"))
		}

		// Verify request body.
		var req messagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if req.Model != defaultModel {
			t.Errorf("expected model %s, got %s", defaultModel, req.Model)
		}
		if len(req.Messages) != 1 || req.Messages[0].Content != "test prompt" {
			t.Errorf("unexpected messages: %+v", req.Messages)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`{"answer":"42","score":100}`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.Complete(context.Background(), "test prompt", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "42" || result.Score != 100 {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestCompleteWithSystem_IncludesSystemPrompt(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req messagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if req.System != "you are a judge" {
			t.Errorf("expected system prompt, got %q", req.System)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`{"answer":"yes","score":1}`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.CompleteWithSystem(context.Background(), "you are a judge", "is this good?", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestComplete_AuthError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-bad-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid api key"}`))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	err = c.Complete(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T: %v", err, err)
	}
}

func TestComplete_RateLimitRetries(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`{"answer":"ok","score":1}`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.Complete(context.Background(), "test", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "ok" {
		t.Errorf("expected answer 'ok', got %q", result.Answer)
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 calls, got %d", callCount.Load())
	}
}

func TestComplete_ServerErrorRetries(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := callCount.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`internal error`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`{"answer":"recovered","score":1}`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.Complete(context.Background(), "test", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "recovered" {
		t.Errorf("expected answer 'recovered', got %q", result.Answer)
	}
}

func TestComplete_ExhaustedRetries(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`rate limited`))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	err = c.Complete(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
}

func TestComplete_NonRetryableError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`bad request`))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	err = c.Complete(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %T: %v", err, err)
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 call (no retries for 400), got %d", callCount.Load())
	}
}

func TestComplete_ContextCancellation(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var result testResult
	err = c.Complete(ctx, "test", &result)
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestComplete_ParseError_InvalidJSON(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`not valid json at all`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	err = c.Complete(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected parse error")
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestComplete_ParseError_EmptyContent(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := messagesResponse{
			Content:    []contentBlock{},
			StopReason: "end_turn",
		}
		data, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	err = c.Complete(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected parse error for empty content")
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestComplete_MarkdownFencedJSON(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fenced := "```json\n{\"answer\":\"fenced\",\"score\":99}\n```"
		_, _ = w.Write([]byte(makeMessagesResponse(fenced)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.Complete(context.Background(), "test", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "fenced" || result.Score != 99 {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestComplete_ForbiddenError(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	err = c.Complete(context.Background(), "test", &result)
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError for 403, got %T: %v", err, err)
	}
}

// --- FakeClient tests ---

func TestFakeClient_Complete(t *testing.T) {
	fake := &FakeClient{
		Response: testResult{Answer: "fake", Score: 42},
	}

	var result testResult
	if err := fake.Complete(context.Background(), "test prompt", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "fake" || result.Score != 42 {
		t.Errorf("unexpected result: %+v", result)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].Prompt != "test prompt" {
		t.Errorf("expected prompt 'test prompt', got %q", fake.Calls[0].Prompt)
	}
}

func TestFakeClient_CompleteWithSystem(t *testing.T) {
	fake := &FakeClient{
		Response: testResult{Answer: "ok", Score: 1},
	}

	var result testResult
	if err := fake.CompleteWithSystem(context.Background(), "sys", "prompt", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.Calls[0].System != "sys" {
		t.Errorf("expected system 'sys', got %q", fake.Calls[0].System)
	}
}

func TestFakeClient_Error(t *testing.T) {
	fake := &FakeClient{
		Err: &AuthError{Message: "no key"},
	}

	var result testResult
	err := fake.Complete(context.Background(), "test", &result)
	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T: %v", err, err)
	}
}

// --- Error type tests ---

func TestErrorTypes(t *testing.T) {
	t.Run("AuthError", func(t *testing.T) {
		err := &AuthError{Message: "missing key"}
		if err.Error() != "auth error: missing key" {
			t.Errorf("unexpected error string: %s", err.Error())
		}
	})

	t.Run("RateLimitError", func(t *testing.T) {
		err := &RateLimitError{RetryAfter: 5 * time.Second}
		if err.Error() != "rate limit exceeded, retry after 5s" {
			t.Errorf("unexpected error string: %s", err.Error())
		}
	})

	t.Run("ParseError", func(t *testing.T) {
		inner := errors.New("bad json")
		err := &ParseError{Body: "xyz", Err: inner}
		if err.Unwrap() != inner {
			t.Errorf("Unwrap returned wrong error")
		}
	})

	t.Run("APIError", func(t *testing.T) {
		err := &APIError{StatusCode: 502, Body: "bad gateway"}
		if err.Error() != "api error (status 502): bad gateway" {
			t.Errorf("unexpected error string: %s", err.Error())
		}
	})
}

// --- Tool-use tests ---

func TestCompleteWithTool_Success(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	tool := Tool{
		Name:        "submit_verdict",
		Description: "submit a verdict",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}`),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req messagesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		if len(req.Tools) != 1 || req.Tools[0].Name != "submit_verdict" {
			t.Errorf("expected tools=[submit_verdict], got %+v", req.Tools)
		}
		if req.ToolChoice == nil || req.ToolChoice.Type != "tool" || req.ToolChoice.Name != "submit_verdict" {
			t.Errorf("expected forced tool_choice for submit_verdict, got %+v", req.ToolChoice)
		}

		resp := messagesResponse{
			Content: []contentBlock{{
				Type:  "tool_use",
				Name:  "submit_verdict",
				Input: json.RawMessage(`{"answer":"structured"}`),
			}},
			StopReason: "tool_use",
		}
		data, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.CompleteWithTool(context.Background(), "sys", "prompt", tool, &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "structured" {
		t.Errorf("expected answer from tool_use block, got %q", result.Answer)
	}
}

func TestCompleteWithTool_MissingToolBlock(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`plain text, no tool_use`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	err = c.CompleteWithTool(context.Background(), "", "prompt",
		Tool{Name: "submit_verdict", InputSchema: json.RawMessage(`{}`)}, &result)
	if err == nil {
		t.Fatal("expected ParseError when no tool_use block is returned")
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestWithTemperature_IncludedInRequest(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	var captured messagesRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`{"answer":"ok","score":1}`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL), WithTemperature(0))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.Complete(context.Background(), "prompt", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured.Temperature == nil {
		t.Fatal("expected temperature to be set in request")
	}
	if *captured.Temperature != 0 {
		t.Errorf("expected temperature=0, got %f", *captured.Temperature)
	}
}

func TestWithoutTemperature_OmitsField(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	var bodyBytes []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyBytes = b
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(`{"answer":"ok","score":1}`)))
	}))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.Complete(context.Background(), "prompt", &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(string(bodyBytes), `"temperature"`) {
		t.Errorf("expected temperature field omitted when unset, got body: %s", bodyBytes)
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain json",
			input: `{"key":"value"}`,
			want:  `{"key":"value"}`,
		},
		{
			name:  "fenced json",
			input: "```json\n{\"key\":\"value\"}\n```",
			want:  "{\"key\":\"value\"}\n",
		},
		{
			name:  "plain fenced",
			input: "```\n{\"key\":\"value\"}\n```",
			want:  "{\"key\":\"value\"}\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
