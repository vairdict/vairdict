package claude

import (
	"context"
	"encoding/json"
	"fmt"
)

// FakeClient is a test double for the Claude API client that returns
// configurable responses without making network calls.
type FakeClient struct {
	// Response is the object that will be JSON-marshalled and then
	// unmarshalled into the target on each Complete call.
	Response any

	// Err, if set, is returned from every Complete call.
	Err error

	// Calls records each prompt sent to Complete for assertions.
	Calls []FakeCall
}

// FakeCall records a single invocation of Complete, CompleteWithSystem, or
// CompleteWithTool. ToolName is empty for non-tool calls.
type FakeCall struct {
	System   string
	Prompt   string
	ToolName string
}

// Complete records the call and returns the configured response or error.
func (f *FakeClient) Complete(_ context.Context, prompt string, target any) error {
	return f.CompleteWithSystem(context.Background(), "", prompt, target)
}

// CompleteWithSystem records the call and returns the configured response or error.
func (f *FakeClient) CompleteWithSystem(_ context.Context, system, prompt string, target any) error {
	f.Calls = append(f.Calls, FakeCall{System: system, Prompt: prompt})
	return f.fill(target)
}

// CompleteWithTool records the call (including the tool name) and returns the
// configured response or error. The configured Response is marshalled into the
// target as if the tool had been invoked.
func (f *FakeClient) CompleteWithTool(_ context.Context, system, prompt string, tool Tool, target any) error {
	f.Calls = append(f.Calls, FakeCall{System: system, Prompt: prompt, ToolName: tool.Name})
	return f.fill(target)
}

// CompleteWithTools records the call (using finalTool as ToolName) and returns
// the configured response. Does not simulate the multi-turn loop.
func (f *FakeClient) CompleteWithTools(_ context.Context, system, prompt string, _ []Tool, finalTool string, _ map[string]ToolHandler, target any) error {
	f.Calls = append(f.Calls, FakeCall{System: system, Prompt: prompt, ToolName: finalTool})
	return f.fill(target)
}

func (f *FakeClient) fill(target any) error {
	if f.Err != nil {
		return f.Err
	}

	data, err := json.Marshal(f.Response)
	if err != nil {
		return fmt.Errorf("fake client: marshalling response: %w", err)
	}

	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("fake client: unmarshalling into target: %w", err)
	}

	return nil
}
