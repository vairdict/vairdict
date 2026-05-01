package claude

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// CompleteWithSystemStream is the streaming variant of CompleteWithSystem.
// When onDelta is non-nil, every text_delta from the SSE response is fed
// to it as the model produces output. The accumulated text is then
// extracted and unmarshalled into target the same way the non-streaming
// path does. onDelta may be nil — in that case the call still streams
// (paying the SSE overhead) but the caller sees only the final result.
//
// Streaming changes total wall-clock time only marginally; the benefit is
// perceived latency — the user watches the planner output form in real
// time instead of staring at "generating plan" until the final message
// returns.
//
// Errors mirror the non-streaming path: AuthError on 401/403, APIError
// on other non-2xx responses pre-stream, ParseError when accumulated
// text cannot be decoded into target. SSE-specific failures (malformed
// data lines, missing message_stop) surface as ParseError. Network
// failures pre-stream are NOT retried — the caller is expected to use
// the non-streaming path when retry semantics matter.
func (c *Client) CompleteWithSystemStream(
	ctx context.Context,
	system, prompt string,
	target any,
	onDelta func(text string),
) error {
	reqBody := streamingRequest{
		messagesRequest: messagesRequest{
			Model:       c.model,
			MaxTokens:   c.maxTokens,
			System:      systemPayload(system),
			Messages:    []message{{Role: "user", Content: prompt}},
			Temperature: c.temperature,
		},
		Stream: true,
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshalling streaming request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("creating streaming request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", defaultAnthropicVersion)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending streaming request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK:
		// fall through to SSE consumer
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		body, _ := io.ReadAll(resp.Body)
		return &AuthError{Message: string(body)}
	default:
		body, _ := io.ReadAll(resp.Body)
		return &APIError{StatusCode: resp.StatusCode, Body: string(body)}
	}

	text, finalUsage, err := consumeSSE(resp.Body, onDelta)
	if err != nil {
		return err
	}
	logUsage(c.model, finalUsage)

	cleaned := extractJSON(text)
	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		return &ParseError{Body: text, Err: fmt.Errorf("unmarshalling streamed text: %w", err)}
	}
	return nil
}

// streamingRequest embeds messagesRequest and adds the stream flag. A
// dedicated type keeps the non-streaming request body unchanged so
// existing tests that decode messagesRequest don't see an unexpected
// field.
type streamingRequest struct {
	messagesRequest
	Stream bool `json:"stream"`
}

// consumeSSE reads the SSE response body, accumulating text_delta
// payloads into a single string and feeding each delta to onDelta as
// it arrives. Returns the accumulated text plus the final merged usage
// snapshot (input_tokens + cache fields from message_start, plus the
// final output_tokens from message_delta).
func consumeSSE(body io.Reader, onDelta func(text string)) (string, usage, error) {
	var (
		acc      strings.Builder
		finalUsage usage
		sawStop  bool
	)

	// The Anthropic SSE format wraps each event in two lines:
	//   event: <name>
	//   data: <json>
	//
	// Followed by a blank line. We only need the data lines — the type
	// field inside the JSON tells us which event it was.
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "" || payload == "[DONE]" {
			continue
		}

		var ev streamEvent
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return "", usage{}, &ParseError{Body: payload, Err: fmt.Errorf("decoding sse event: %w", err)}
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				finalUsage.InputTokens = ev.Message.Usage.InputTokens
				finalUsage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
				finalUsage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
				finalUsage.OutputTokens = ev.Message.Usage.OutputTokens
			}
		case "content_block_delta":
			if ev.Delta != nil && ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
				acc.WriteString(ev.Delta.Text)
				if onDelta != nil {
					onDelta(ev.Delta.Text)
				}
			}
		case "message_delta":
			// message_delta carries the cumulative output_tokens; the
			// final value here is the authoritative count.
			if ev.Usage != nil {
				finalUsage.OutputTokens = ev.Usage.OutputTokens
			}
		case "message_stop":
			sawStop = true
		case "error":
			if ev.Error != nil {
				return "", usage{}, &APIError{StatusCode: 0, Body: ev.Error.Message}
			}
			return "", usage{}, &APIError{StatusCode: 0, Body: payload}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", usage{}, fmt.Errorf("reading sse stream: %w", err)
	}
	if !sawStop {
		return "", usage{}, &ParseError{Body: acc.String(), Err: fmt.Errorf("sse stream ended without message_stop")}
	}
	return acc.String(), finalUsage, nil
}

// streamEvent is the union type for every SSE event payload we care
// about. Fields not relevant to the dispatched type stay nil.
type streamEvent struct {
	Type    string             `json:"type"`
	Message *streamMessageInfo `json:"message,omitempty"`
	Delta   *streamDelta       `json:"delta,omitempty"`
	Usage   *streamUsage       `json:"usage,omitempty"`
	Error   *streamError       `json:"error,omitempty"`
}

type streamMessageInfo struct {
	Usage usage `json:"usage"`
}

type streamDelta struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type streamUsage struct {
	OutputTokens int `json:"output_tokens"`
}

type streamError struct {
	Message string `json:"message"`
}
