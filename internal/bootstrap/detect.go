package bootstrap

import (
	"log/slog"
	"os"
	"path/filepath"
)

// DetectionResult holds everything we could infer from the repo.
type DetectionResult struct {
	Language  string
	Formatter string
	Linter    string
}

// Detect inspects the directory for language, formatter, and linter hints.
func Detect(dir string) DetectionResult {
	r := DetectionResult{
		Language:  DetectLanguage(dir),
		Formatter: DetectFormatter(dir),
		Linter:    DetectLinter(dir),
	}
	slog.Debug("detection result", "language", r.Language, "formatter", r.Formatter, "linter", r.Linter)
	return r
}

// DetectLanguage checks for language marker files and returns a language name
// or empty string if none detected.
func DetectLanguage(dir string) string {
	markers := []struct {
		file string
		lang string
	}{
		{"go.mod", "go"},
		{"package.json", "javascript"},
		{"Cargo.toml", "rust"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"Gemfile", "ruby"},
	}

	for _, m := range markers {
		if fileExists(filepath.Join(dir, m.file)) {
			slog.Debug("detected language", "file", m.file, "language", m.lang)
			return m.lang
		}
	}
	return ""
}

// DetectFormatter checks for formatter config files and returns a formatter name
// or empty string if none detected.
func DetectFormatter(dir string) string {
	formatters := []struct {
		file      string
		formatter string
	}{
		{".prettierrc", "prettier"},
		{".prettierrc.json", "prettier"},
		{".prettierrc.yaml", "prettier"},
		{".prettierrc.yml", "prettier"},
		{"prettier.config.js", "prettier"},
		{".rustfmt.toml", "rustfmt"},
		{"rustfmt.toml", "rustfmt"},
		{".editorconfig", "editorconfig"},
	}

	for _, f := range formatters {
		if fileExists(filepath.Join(dir, f.file)) {
			slog.Debug("detected formatter", "file", f.file, "formatter", f.formatter)
			return f.formatter
		}
	}

	// Go projects use gofmt by default.
	if fileExists(filepath.Join(dir, "go.mod")) {
		return "gofmt"
	}

	return ""
}

// DetectLinter checks for linter config files and returns a linter name
// or empty string if none detected.
func DetectLinter(dir string) string {
	linters := []struct {
		file   string
		linter string
	}{
		{".golangci.yml", "golangci-lint"},
		{".golangci.yaml", "golangci-lint"},
		{".golangci.toml", "golangci-lint"},
		{".eslintrc", "eslint"},
		{".eslintrc.json", "eslint"},
		{".eslintrc.js", "eslint"},
		{".eslintrc.yml", "eslint"},
		{"eslint.config.js", "eslint"},
		{"eslint.config.mjs", "eslint"},
		{".flake8", "flake8"},
		{"setup.cfg", "flake8"},
		{"pyproject.toml", "ruff"}, // modern Python projects often use ruff
		{"clippy.toml", "clippy"},
		{".clippy.toml", "clippy"},
		{".rubocop.yml", "rubocop"},
	}

	for _, l := range linters {
		if fileExists(filepath.Join(dir, l.file)) {
			slog.Debug("detected linter", "file", l.file, "linter", l.linter)
			return l.linter
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
