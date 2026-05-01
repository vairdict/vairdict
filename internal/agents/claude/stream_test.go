package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// sseHandler builds an http.Handler that writes the given event lines as
// SSE payloads, separated by blank lines. Each entry is a (eventName,
// jsonData) pair. The handler also asserts that the request body
// included `"stream":true`.
func sseHandler(t *testing.T, events []sseEvent) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Errorf("expected stream:true in request body, got: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, ev := range events {
			fmt.Fprintf(w, "event: %s\n", ev.event)
			fmt.Fprintf(w, "data: %s\n\n", ev.data)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

type sseEvent struct {
	event string
	data  string
}

// recordedSSE returns a typical Anthropic stream that emits "Hello, "
// then "world!" as two text deltas, then a final stop event.
func recordedSSE(textChunks []string) []sseEvent {
	events := []sseEvent{
		{
			event: "message_start",
			data: `{"type":"message_start","message":{"id":"msg_test","usage":{"input_tokens":42,"output_tokens":0,"cache_creation_input_tokens":1500,"cache_read_input_tokens":0}}}`,
		},
		{
			event: "content_block_start",
			data:  `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		},
	}
	for _, chunk := range textChunks {
		// JSON-encode the chunk so we don't have to think about escaping.
		jsonChunk, _ := json.Marshal(chunk)
		events = append(events, sseEvent{
			event: "content_block_delta",
			data:  fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`, jsonChunk),
		})
	}
	events = append(events,
		sseEvent{event: "content_block_stop", data: `{"type":"content_block_stop","index":0}`},
		sseEvent{event: "message_delta", data: `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":17}}`},
		sseEvent{event: "message_stop", data: `{"type":"message_stop"}`},
	)
	return events
}

func TestCompleteWithSystemStream_AccumulatesDeltas(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	chunks := []string{`{"answer":"`, `streamed`, `","score":7}`}
	srv := httptest.NewServer(sseHandler(t, recordedSSE(chunks)))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var deltas []string
	var mu sync.Mutex
	onDelta := func(text string) {
		mu.Lock()
		deltas = append(deltas, text)
		mu.Unlock()
	}

	var result testResult
	if err := c.CompleteWithSystemStream(context.Background(), "", "prompt", &result, onDelta); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Answer != "streamed" || result.Score != 7 {
		t.Errorf("unexpected accumulated result: %+v", result)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(deltas) != len(chunks) {
		t.Fatalf("expected %d deltas, got %d: %#v", len(chunks), len(deltas), deltas)
	}
	for i, want := range chunks {
		if deltas[i] != want {
			t.Errorf("delta[%d] = %q, want %q", i, deltas[i], want)
		}
	}
}

func TestCompleteWithSystemStream_NilOnDeltaIsOK(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	srv := httptest.NewServer(sseHandler(t, recordedSSE([]string{`{"answer":"`, `nodelta`, `","score":1}`})))
	defer srv.Close()

	c, err := NewClient(nil, WithEndpoint(srv.URL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result testResult
	if err := c.CompleteWithSystemStream(context.Background(), "", "p", &result, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Answer != "nodelta" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestCompleteWithSystemStream_MatchesNonStreamingResult(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Two servers — one streaming SSE, one returning the same content
	// as a single completed messagesResponse. The accumulated text
	// must produce the same target struct.
	const finalJSON = `{"answer":"parity","score":42}`

	streamSrv := httptest.NewServer(sseHandler(t, recordedSSE([]string{
		`{"answer":"`, `parity`, `","score":42}`,
	})))
	defer streamSrv.Close()

	blockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(makeMessagesResponse(finalJSON)))
	}))
	defer blockSrv.Close()

	streamClient, _ := NewClient(nil, WithEndpoint(streamSrv.URL))
	blockClient, _ := NewClient(nil, WithEndpoint(blockSrv.URL))

	var streamed testResult
	if err := streamClient.CompleteWithSystemStream(context.Background(), "", "p", &streamed, nil); err != nil {
		t.Fatalf("streaming: %v", err)
	}
	var blocked testResult
	if err := blockClient.CompleteWithSystem(context.Background(), "", "p", &blocked); err != nil {
		t.Fatalf("blocking: %v", err)
	}
	if streamed != blocked {
		t.Errorf("parity mismatch: streamed=%+v blocked=%+v", streamed, blocked)
	}
}

func TestCompleteWithSystemStream_AuthErrorBeforeStream(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-bad")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"bad key"}`))
	}))
	defer srv.Close()

	c, _ := NewClient(nil, WithEndpoint(srv.URL))
	var result testResult
	err := c.CompleteWithSystemStream(context.Background(), "", "p", &result, nil)
	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected AuthError, got %T: %v", err, err)
	}
}

func TestCompleteWithSystemStream_MissingMessageStop(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	// Truncated stream — no message_stop event. Caller must see
	// ParseError so they don't silently consume a half-finished plan.
	events := []sseEvent{
		{event: "message_start", data: `{"type":"message_start","message":{"usage":{}}}`},
		{event: "content_block_delta", data: `{"type":"content_block_delta","delta":{"type":"text_delta","text":"abc"}}`},
		// no message_stop
	}
	srv := httptest.NewServer(sseHandler(t, events))
	defer srv.Close()

	c, _ := NewClient(nil, WithEndpoint(srv.URL))
	var result testResult
	err := c.CompleteWithSystemStream(context.Background(), "", "p", &result, nil)
	if err == nil {
		t.Fatal("expected error on truncated stream")
	}
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestCompleteWithSystemStream_HonorsCachePayload(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")

	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, ev := range recordedSSE([]string{`{"answer":"ok","score":1}`}) {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.event, ev.data)
		}
	}))
	defer srv.Close()

	c, _ := NewClient(nil, WithEndpoint(srv.URL))
	var result testResult
	if err := c.CompleteWithSystemStream(context.Background(), largeSystemPrompt(), "p", &result, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(rawBody), `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("expected cache_control marker on streaming call, got body head: %s", truncateForLog(string(rawBody), 400))
	}
}
