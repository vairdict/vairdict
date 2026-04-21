package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	return path
}

func TestLoadManifest_Happy(t *testing.T) {
	path := writeManifest(t, `
tasks:
  - name: a
    intent: "Do the first thing"
  - name: b
    intent: "Do the second thing"
    depends_on: [a]
`)
	m, err := LoadManifest(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.Tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(m.Tasks))
	}
	if m.Tasks[1].DependsOn[0] != "a" {
		t.Errorf("expected b depends on a, got %v", m.Tasks[1].DependsOn)
	}
}

func TestLoadManifest_EmptyList_Rejected(t *testing.T) {
	path := writeManifest(t, "tasks: []\n")
	if _, err := LoadManifest(path); err == nil {
		t.Error("expected error for empty task list")
	}
}

func TestLoadManifest_MissingName_Rejected(t *testing.T) {
	path := writeManifest(t, `
tasks:
  - intent: "no name"
`)
	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("expected 'name is required' error, got %v", err)
	}
}

func TestLoadManifest_DuplicateNames_Rejected(t *testing.T) {
	path := writeManifest(t, `
tasks:
  - name: a
    intent: x
  - name: a
    intent: y
`)
	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate name") {
		t.Errorf("expected duplicate-name error, got %v", err)
	}
}

func TestLoadManifest_InvalidName_Rejected(t *testing.T) {
	path := writeManifest(t, `
tasks:
  - name: "has spaces"
    intent: x
`)
	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "[a-zA-Z0-9_-]+") {
		t.Errorf("expected invalid-name error, got %v", err)
	}
}

func TestLoadManifest_DepRefersToUnknownName(t *testing.T) {
	path := writeManifest(t, `
tasks:
  - name: a
    intent: x
    depends_on: [nope]
`)
	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "not a task in this manifest") {
		t.Errorf("expected unknown-dep error, got %v", err)
	}
}

func TestLoadManifest_SelfDep_Rejected(t *testing.T) {
	path := writeManifest(t, `
tasks:
  - name: a
    intent: x
    depends_on: [a]
`)
	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "cannot depend on itself") {
		t.Errorf("expected self-dep error, got %v", err)
	}
}

func TestLoadManifest_MissingIntent_Rejected(t *testing.T) {
	path := writeManifest(t, `
tasks:
  - name: a
`)
	_, err := LoadManifest(path)
	if err == nil || !strings.Contains(err.Error(), "intent is required") {
		t.Errorf("expected intent-required error, got %v", err)
	}
}
