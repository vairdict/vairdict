package claudecode

import (
	"context"

	"github.com/vairdict/vairdict/internal/state"
)

// FakeRunner is a test double for the Claude Code CLI runner.
type FakeRunner struct {
	// Result is returned from every Run call.
	Result state.AgentResult

	// Err, if set, is returned from every Run call.
	Err error

	// Calls records each invocation for assertions.
	Calls []FakeCall
}

// FakeCall records a single invocation of Run.
type FakeCall struct {
	Prompt  string
	WorkDir string
}

// Run records the call and returns the configured result or error.
func (f *FakeRunner) Run(_ context.Context, prompt string, workDir string) (state.AgentResult, error) {
	f.Calls = append(f.Calls, FakeCall{Prompt: prompt, WorkDir: workDir})

	if f.Err != nil {
		return state.AgentResult{}, f.Err
	}

	return f.Result, nil
}
