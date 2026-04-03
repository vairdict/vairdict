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

// FakeCall records a single invocation of Complete or CompleteWithSystem.
type FakeCall struct {
	System string
	Prompt string
}

// Complete records the call and returns the configured response or error.
func (f *FakeClient) Complete(_ context.Context, prompt string, target any) error {
	return f.CompleteWithSystem(context.Background(), "", prompt, target)
}

// CompleteWithSystem records the call and returns the configured response or error.
func (f *FakeClient) CompleteWithSystem(_ context.Context, system, prompt string, target any) error {
	f.Calls = append(f.Calls, FakeCall{System: system, Prompt: prompt})

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
