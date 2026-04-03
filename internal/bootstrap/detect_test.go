package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0644); err != nil {
		t.Fatalf("creating file %s: %v", path, err)
	}
}

func TestDetectLanguage_Go(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "go.mod"))

	if lang := DetectLanguage(dir); lang != "go" {
		t.Errorf("DetectLanguage = %q, want %q", lang, "go")
	}
}

func TestDetectLanguage_JavaScript(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "package.json"))

	if lang := DetectLanguage(dir); lang != "javascript" {
		t.Errorf("DetectLanguage = %q, want %q", lang, "javascript")
	}
}

func TestDetectLanguage_Rust(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "Cargo.toml"))

	if lang := DetectLanguage(dir); lang != "rust" {
		t.Errorf("DetectLanguage = %q, want %q", lang, "rust")
	}
}

func TestDetectLanguage_Python(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "pyproject.toml"))

	if lang := DetectLanguage(dir); lang != "python" {
		t.Errorf("DetectLanguage = %q, want %q", lang, "python")
	}
}

func TestDetectLanguage_PythonRequirements(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "requirements.txt"))

	if lang := DetectLanguage(dir); lang != "python" {
		t.Errorf("DetectLanguage = %q, want %q", lang, "python")
	}
}

func TestDetectLanguage_Java(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "pom.xml"))

	if lang := DetectLanguage(dir); lang != "java" {
		t.Errorf("DetectLanguage = %q, want %q", lang, "java")
	}
}

func TestDetectLanguage_Ruby(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "Gemfile"))

	if lang := DetectLanguage(dir); lang != "ruby" {
		t.Errorf("DetectLanguage = %q, want %q", lang, "ruby")
	}
}

func TestDetectLanguage_Empty(t *testing.T) {
	dir := t.TempDir()

	if lang := DetectLanguage(dir); lang != "" {
		t.Errorf("DetectLanguage = %q, want empty", lang)
	}
}

func TestDetectLanguage_Priority(t *testing.T) {
	// go.mod comes first in the marker list, so Go wins.
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "go.mod"))
	touch(t, filepath.Join(dir, "package.json"))

	if lang := DetectLanguage(dir); lang != "go" {
		t.Errorf("DetectLanguage = %q, want %q (go.mod takes priority)", lang, "go")
	}
}

func TestDetectFormatter_Prettier(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, ".prettierrc"))

	if f := DetectFormatter(dir); f != "prettier" {
		t.Errorf("DetectFormatter = %q, want %q", f, "prettier")
	}
}

func TestDetectFormatter_Rustfmt(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "rustfmt.toml"))

	if f := DetectFormatter(dir); f != "rustfmt" {
		t.Errorf("DetectFormatter = %q, want %q", f, "rustfmt")
	}
}

func TestDetectFormatter_GoDefault(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "go.mod"))

	if f := DetectFormatter(dir); f != "gofmt" {
		t.Errorf("DetectFormatter = %q, want %q", f, "gofmt")
	}
}

func TestDetectFormatter_Empty(t *testing.T) {
	dir := t.TempDir()

	if f := DetectFormatter(dir); f != "" {
		t.Errorf("DetectFormatter = %q, want empty", f)
	}
}

func TestDetectLinter_GolangciLint(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, ".golangci.yml"))

	if l := DetectLinter(dir); l != "golangci-lint" {
		t.Errorf("DetectLinter = %q, want %q", l, "golangci-lint")
	}
}

func TestDetectLinter_ESLint(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, ".eslintrc.json"))

	if l := DetectLinter(dir); l != "eslint" {
		t.Errorf("DetectLinter = %q, want %q", l, "eslint")
	}
}

func TestDetectLinter_Ruff(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "pyproject.toml"))

	if l := DetectLinter(dir); l != "ruff" {
		t.Errorf("DetectLinter = %q, want %q", l, "ruff")
	}
}

func TestDetectLinter_Empty(t *testing.T) {
	dir := t.TempDir()

	if l := DetectLinter(dir); l != "" {
		t.Errorf("DetectLinter = %q, want empty", l)
	}
}

func TestDetect_FullGoRepo(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "go.mod"))
	touch(t, filepath.Join(dir, ".golangci.yml"))

	r := Detect(dir)
	if r.Language != "go" {
		t.Errorf("Language = %q, want %q", r.Language, "go")
	}
	if r.Formatter != "gofmt" {
		t.Errorf("Formatter = %q, want %q", r.Formatter, "gofmt")
	}
	if r.Linter != "golangci-lint" {
		t.Errorf("Linter = %q, want %q", r.Linter, "golangci-lint")
	}
}

func TestDetect_EmptyRepo(t *testing.T) {
	dir := t.TempDir()

	r := Detect(dir)
	if r.Language != "" {
		t.Errorf("Language = %q, want empty", r.Language)
	}
	if r.Formatter != "" {
		t.Errorf("Formatter = %q, want empty", r.Formatter)
	}
	if r.Linter != "" {
		t.Errorf("Linter = %q, want empty", r.Linter)
	}
}
