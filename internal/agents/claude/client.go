// Package claude provides an Anthropic API client with structured output support.
//
// All agent interactions with the Anthropic Messages API go through this client.
// The client supports typed responses via JSON schema constraints in prompts,
// retry logic with exponential backoff, and context cancellation.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/vairdict/vairdict/internal/config"
)

const (
	defaultEndpoint         = "https://api.anthropic.com/v1/messages"
	defaultModel            = "claude-sonnet-4-20250514"
	defaultAnthropicVersion = "2023-06-01"
	defaultMaxTokens        = 4096
	maxRetries              = 3
	baseBackoff             = 1 * time.Second
)

// AuthError is returned when the API key is missing or rejected.
type AuthError struct {
	Message string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("auth error: %s", e.Message)
}

// RateLimitError is returned when the API returns 429.
type RateLimitError struct {
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limit exceeded, retry after %s", e.RetryAfter)
}

// ParseError is returned when the response cannot be unmarshalled into the target schema.
type ParseError struct {
	Body string
	Err  error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("parse error: %s (body: %.200s)", e.Err, e.Body)
}

func (e *ParseError) Unwrap() error {
	return e.Err
}

// APIError is returned for non-retryable API errors.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("api error (status %d): %.200s", e.StatusCode, e.Body)
}

// messagesRequest is the request body for the Anthropic Messages API.
type messagesRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesResponse is the response body from the Anthropic Messages API.
type messagesResponse struct {
	Content    []contentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      usage          `json:"usage"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// HTTPClient is the interface for making HTTP requests. This allows injecting
// test doubles.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client communicates with the Anthropic Messages API.
type Client struct {
	apiKey     string
	model      string
	endpoint   string
	httpClient HTTPClient
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets a custom HTTP client (useful for testing).
func WithHTTPClient(c HTTPClient) Option {
	return func(cl *Client) {
		cl.httpClient = c
	}
}

// WithEndpoint overrides the API endpoint (useful for testing).
func WithEndpoint(endpoint string) Option {
	return func(cl *Client) {
		cl.endpoint = endpoint
	}
}

// WithModel overrides the model.
func WithModel(model string) Option {
	return func(cl *Client) {
		cl.model = model
	}
}

// NewClient creates a new Anthropic API client. It resolves the API key from:
// 1. ANTHROPIC_API_KEY environment variable
// 2. ~/.config/vairdict/config.yaml
// Options can override any default.
func NewClient(cfg *config.Config, opts ...Option) (*Client, error) {
	apiKey := config.ResolveAPIKey()
	if apiKey == "" {
		path, _ := config.UserConfigPath()
		return nil, &AuthError{Message: fmt.Sprintf(
			"Anthropic API key not found. Set it via:\n"+
				"  1. export ANTHROPIC_API_KEY=sk-...\n"+
				"  2. vairdict init (writes to %s)", path)}
	}

	model := defaultModel
	if cfg != nil && cfg.Agents.Model != "" {
		model = cfg.Agents.Model
	}

	c := &Client{
		apiKey:     apiKey,
		model:      model,
		endpoint:   defaultEndpoint,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// Complete sends a prompt to the Anthropic Messages API and unmarshals the
// JSON response into the target struct. The prompt should instruct Claude to
// return JSON matching the target's shape.
func (c *Client) Complete(ctx context.Context, prompt string, target any) error {
	return c.CompleteWithSystem(ctx, "", prompt, target)
}

// CompleteWithSystem sends a prompt with a system message to the Anthropic
// Messages API and unmarshals the JSON response into the target struct.
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string, target any) error {
	reqBody := messagesRequest{
		Model:     c.model,
		MaxTokens: defaultMaxTokens,
		System:    system,
		Messages: []message{
			{Role: "user", Content: prompt},
		},
	}

	var lastErr error
	for attempt := range maxRetries {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("completing request: %w", err)
		}

		body, err := c.doRequest(ctx, reqBody)
		if err == nil {
			return c.parseResponse(body, target)
		}

		lastErr = err

		// Only retry on rate-limit or server errors.
		if !isRetryable(err) {
			return err
		}

		backoff := time.Duration(math.Pow(2, float64(attempt))) * baseBackoff
		slog.Warn("retrying anthropic request",
			"attempt", attempt+1,
			"backoff", backoff,
			"error", err,
		)

		select {
		case <-ctx.Done():
			return fmt.Errorf("completing request: %w", ctx.Err())
		case <-time.After(backoff):
		}
	}

	return fmt.Errorf("completing request after %d retries: %w", maxRetries, lastErr)
}

func (c *Client) doRequest(ctx context.Context, reqBody messagesRequest) ([]byte, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", defaultAnthropicVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &AuthError{Message: string(body)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &RateLimitError{RetryAfter: baseBackoff}
	case resp.StatusCode >= 500:
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	default:
		return nil, &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}
}

func (c *Client) parseResponse(body []byte, target any) error {
	var resp messagesResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return &ParseError{Body: string(body), Err: fmt.Errorf("unmarshalling response: %w", err)}
	}

	if len(resp.Content) == 0 {
		return &ParseError{Body: string(body), Err: fmt.Errorf("empty content in response")}
	}

	text := resp.Content[0].Text

	// Try to extract JSON from the response text if it's wrapped in markdown.
	cleaned := extractJSON(text)

	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		return &ParseError{Body: text, Err: fmt.Errorf("unmarshalling into target: %w", err)}
	}

	return nil
}

// extractJSON tries to extract a JSON object or array from text that may be
// wrapped in markdown code fences. If no fences are found, it returns the
// original text trimmed.
func extractJSON(text string) string {
	// Look for ```json ... ``` blocks.
	const fence = "```"
	start := -1
	for i := 0; i <= len(text)-len(fence); i++ {
		if text[i:i+len(fence)] == fence {
			if start == -1 {
				// Find end of opening fence line.
				for j := i + len(fence); j < len(text); j++ {
					if text[j] == '\n' {
						start = j + 1
						break
					}
				}
			} else {
				return text[start:i]
			}
		}
	}
	return text
}

func isRetryable(err error) bool {
	switch e := err.(type) {
	case *RateLimitError:
		return true
	case *APIError:
		return e.StatusCode >= 500
	default:
		return false
	}
}
