package github

import "context"

// FakeRunner records commands and returns configurable responses.
type FakeRunner struct {
	Responses map[string]fakeResponse
	Calls     []FakeCall
}

type fakeResponse struct {
	Output []byte
	Err    error
}

// FakeCall records a command invocation.
type FakeCall struct {
	Name string
	Args []string
}

// Run records the call and returns the configured response.
func (f *FakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.Calls = append(f.Calls, FakeCall{Name: name, Args: args})

	// Build lookup keys from most specific to least.
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	if len(args) > 1 {
		specific := name + " " + args[0] + " " + args[1]
		if resp, ok := f.Responses[specific]; ok {
			return resp.Output, resp.Err
		}
	}
	if resp, ok := f.Responses[key]; ok {
		return resp.Output, resp.Err
	}
	if resp, ok := f.Responses[name]; ok {
		return resp.Output, resp.Err
	}

	return nil, nil
}
