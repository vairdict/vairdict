package main

import (
	"reflect"
	"testing"
)

func TestBackgroundArgsForRun_PassesFlagsAndIntents(t *testing.T) {
	got := backgroundArgsForRun(
		[]string{"add login"},
		[]int{32, 0, 45},
		"ci",
		"high",
		[]string{"abc123", "def456"},
	)
	want := []string{
		"run",
		"--issue", "32",
		"--issue", "45",
		"--env", "ci",
		"--priority", "high",
		"--depends-on", "abc123",
		"--depends-on", "def456",
		"add login",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args mismatch:\n got: %v\nwant: %v", got, want)
	}
}

func TestBackgroundArgsForRun_OmitsEmpty(t *testing.T) {
	got := backgroundArgsForRun([]string{"x"}, nil, "", "", nil)
	want := []string{"run", "x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("args with no flags should be minimal: got %v want %v", got, want)
	}
}

func TestBackgroundArgsForResume(t *testing.T) {
	got := backgroundArgsForResume("a1b2c3d4", "dev")
	want := []string{"resume", "--env", "dev", "a1b2c3d4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}

	got = backgroundArgsForResume("a1b2c3d4", "")
	want = []string{"resume", "a1b2c3d4"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("no-env: got %v want %v", got, want)
	}
}

func TestShouldRunForeground(t *testing.T) {
	t.Setenv("VAIRDICT_FOREGROUND", "1")
	if !shouldRunForeground() {
		t.Error("VAIRDICT_FOREGROUND=1 should force foreground")
	}
	t.Setenv("VAIRDICT_FOREGROUND", "")
	if shouldRunForeground() {
		t.Error("unset VAIRDICT_FOREGROUND should not force foreground")
	}
}
