package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const baseYAML = `
project:
  name: myproject
  type: startup
agents:
  planner: claude
  judge: claude
escalation:
  notify_via: stdout
phases:
  plan:
    max_loops: 3
  code:
    max_loops: 3
  quality:
    max_loops: 3
`

// writeFile is a tiny helper so each test can stage a base + overlay pair
// in a temp directory without leaking files between tests.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestLoadConfigWithOverlay_NoOverlay(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "vairdict.yaml", baseYAML)

	cfg, err := LoadConfigWithOverlay(base, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents.Judge != "claude" {
		t.Errorf("judge = %q, want claude (base unchanged)", cfg.Agents.Judge)
	}
}

func TestLoadConfigWithOverlay_AppliesOverlay(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "vairdict.yaml", baseYAML)
	overlay := writeFile(t, dir, "vairdict.ci.yaml", `
agents:
  planner: claude-api
  judge: claude-api
escalation:
  notify_via: github
`)

	cfg, err := LoadConfigWithOverlay(base, overlay)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents.Planner != "claude-api" {
		t.Errorf("planner = %q, want claude-api", cfg.Agents.Planner)
	}
	if cfg.Agents.Judge != "claude-api" {
		t.Errorf("judge = %q, want claude-api", cfg.Agents.Judge)
	}
	if cfg.Escalation.NotifyVia != "github" {
		t.Errorf("notify_via = %q, want github", cfg.Escalation.NotifyVia)
	}
	// Untouched fields preserved from base.
	if cfg.Project.Name != "myproject" {
		t.Errorf("project.name lost during merge: %q", cfg.Project.Name)
	}
}

func TestLoadConfigWithOverlay_OverlayMissing(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "vairdict.yaml", baseYAML)

	_, err := LoadConfigWithOverlay(base, filepath.Join(dir, "nope.yaml"))
	if err == nil {
		t.Fatal("expected error when overlay path is set but file is missing")
	}
	if !strings.Contains(err.Error(), "reading overlay") {
		t.Errorf("error should mention reading overlay, got: %v", err)
	}
}

func TestLoadConfigWithOverlay_OverlayMalformed(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "vairdict.yaml", baseYAML)
	overlay := writeFile(t, dir, "vairdict.ci.yaml", "agents: [not, a, map]\n")

	_, err := LoadConfigWithOverlay(base, overlay)
	if err == nil {
		t.Fatal("expected error for malformed overlay")
	}
	if !strings.Contains(err.Error(), "parsing overlay") {
		t.Errorf("error should mention parsing overlay, got: %v", err)
	}
}

func TestLoadConfigWithOverlay_BaseInvalid(t *testing.T) {
	dir := t.TempDir()
	// Base missing project.name → validate() rejects.
	base := writeFile(t, dir, "vairdict.yaml", "agents:\n  planner: claude\n")

	_, err := LoadConfigWithOverlay(base, "")
	if err == nil {
		t.Fatal("expected base validation to fail")
	}
}

func TestLoadConfigWithOverlay_PreservesNonOverriddenFields(t *testing.T) {
	dir := t.TempDir()
	base := writeFile(t, dir, "vairdict.yaml", baseYAML+`
commands:
  build: make build
  test: make test
`)
	// Overlay only touches escalation; commands must survive.
	overlay := writeFile(t, dir, "vairdict.ci.yaml", `
escalation:
  notify_via: github
`)

	cfg, err := LoadConfigWithOverlay(base, overlay)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Commands.Build != "make build" {
		t.Errorf("commands.build = %q, want make build", cfg.Commands.Build)
	}
	if cfg.Commands.Test != "make test" {
		t.Errorf("commands.test = %q, want make test", cfg.Commands.Test)
	}
}

func TestResolveOverlayPath(t *testing.T) {
	exists := func(p string) bool { return strings.HasSuffix(p, "vairdict.ci.yaml") }
	never := func(string) bool { return false }

	cases := []struct {
		name     string
		explicit string
		ci       bool
		exists   func(string) bool
		want     string
	}{
		{"explicit wins over everything", "/tmp/custom.yaml", true, exists, "/tmp/custom.yaml"},
		{"explicit wins even when ci off", "/tmp/custom.yaml", false, never, "/tmp/custom.yaml"},
		{"ci + file present picks default", "", true, exists, filepath.Join(".", "vairdict.ci.yaml")},
		{"ci but no file", "", true, never, ""},
		{"no ci no explicit", "", false, exists, ""},
		{"nil exists treated as false", "", true, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveOverlayPath(tc.explicit, tc.ci, ".", tc.exists)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsCI(t *testing.T) {
	t.Setenv("CI", "true")
	if !IsCI() {
		t.Error("CI=true should be detected")
	}
	t.Setenv("CI", "1")
	if !IsCI() {
		t.Error("CI=1 should be detected")
	}
	t.Setenv("CI", "")
	if IsCI() {
		t.Error("CI empty should not be detected")
	}
	t.Setenv("CI", "false")
	if IsCI() {
		t.Error("CI=false should not be detected")
	}
}
