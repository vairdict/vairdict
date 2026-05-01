package github

import "context"

// FakeRunner records commands and returns configurable responses.
type FakeRunner struct {
	// Responses maps a command key to a single canned response. Reused
	// across every matching call.
	Responses map[string]fakeResponse
	// Sequence is consumed in order: each matching call pops the head
	// of the slice. Useful for paginated APIs or list-then-resolve
	// flows where the same command shape is called multiple times
	// with different arguments. Sequence wins over Responses when both
	// match the same key.
	Sequence map[string][]fakeResponse
	Calls    []FakeCall
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

	keys := []string{name}
	if len(args) > 0 {
		keys = append([]string{name + " " + args[0]}, keys...)
	}
	if len(args) > 1 {
		keys = append([]string{name + " " + args[0] + " " + args[1]}, keys...)
	}

	// Sequence wins over Responses so tests can drive multi-call
	// flows (pagination, list-then-resolve) without bespoke runners.
	for _, k := range keys {
		if resp, ok := popSeq(f.Sequence, k); ok {
			return resp.Output, resp.Err
		}
	}
	for _, k := range keys {
		if resp, ok := f.Responses[k]; ok {
			return resp.Output, resp.Err
		}
	}
	return nil, nil
}

// popSeq pulls the head of the queue at key, mutating the map. Returns
// (zero, false) when the key is missing or its queue is empty so the
// caller can fall through to the single-shot Responses map.
func popSeq(m map[string][]fakeResponse, key string) (fakeResponse, bool) {
	if m == nil {
		return fakeResponse{}, false
	}
	seq := m[key]
	if len(seq) == 0 {
		return fakeResponse{}, false
	}
	resp := seq[0]
	m[key] = seq[1:]
	return resp, true
}
