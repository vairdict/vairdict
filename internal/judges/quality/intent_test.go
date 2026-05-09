package quality

import (
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
)

// dffOnly builds a minimal unified diff containing just the headers for
// the given files. Enough for IsSourceDiff to extract the paths.
func dffOnly(files ...string) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString("diff --git a/" + f + " b/" + f + "\n")
		b.WriteString("--- a/" + f + "\n")
		b.WriteString("+++ b/" + f + "\n")
		b.WriteString("@@ -1 +1 @@\n-old\n+new\n")
	}
	return b.String()
}

func TestIsSourceDiff(t *testing.T) {
	cases := []struct {
		name     string
		diff     string
		commands config.CommandsConfig
		want     bool
	}{
		{
			name: "go ./... — no concrete paths derivable, default true",
			diff: dffOnly("plans/PROGRESS.md", "plans/ROADMAP.md"),
			commands: config.CommandsConfig{
				Build: "go build ./...",
				Test:  "go test ./...",
				Lint:  "golangci-lint run ./...",
			},
			want: true,
		},
		{
			name: "make build — no derivable paths, default true",
			diff: dffOnly("README.md"),
			commands: config.CommandsConfig{
				Build: "make build",
				Test:  "make test",
				Lint:  "make lint",
			},
			want: true,
		},
		{
			name:     "empty commands → default true with log",
			diff:     dffOnly("README.md"),
			commands: config.CommandsConfig{},
			want:     true,
		},
		{
			name: "pytest tests/ — diff only docs → false",
			diff: dffOnly("README.md", "docs/intro.md"),
			commands: config.CommandsConfig{
				Test: "pytest tests/",
			},
			want: false,
		},
		{
			name: "pytest tests/ — diff under tests/ → true",
			diff: dffOnly("tests/test_foo.py"),
			commands: config.CommandsConfig{
				Test: "pytest tests/",
			},
			want: true,
		},
		{
			name: "eslint src/**/*.ts — only docs changed → false",
			diff: dffOnly("README.md", "CHANGELOG.md"),
			commands: config.CommandsConfig{
				Lint: "eslint src/**/*.ts",
			},
			want: false,
		},
		{
			name: "eslint src/ — touches src → true",
			diff: dffOnly("src/index.ts"),
			commands: config.CommandsConfig{
				Lint: "eslint src/",
			},
			want: true,
		},
		{
			name: "go test ./internal/... ./cmd/... — touches cmd → true",
			diff: dffOnly("cmd/main.go"),
			commands: config.CommandsConfig{
				Test: "go test ./internal/... ./cmd/...",
			},
			want: true,
		},
		{
			name: "go test ./internal/... ./cmd/... — only README → false",
			diff: dffOnly("README.md"),
			commands: config.CommandsConfig{
				Test: "go test ./internal/... ./cmd/...",
			},
			want: false,
		},
		{
			name: "mixed — one source one docs → true",
			diff: dffOnly("plans/PROGRESS.md", "internal/foo.go"),
			commands: config.CommandsConfig{
				Test: "go test ./internal/...",
			},
			want: true,
		},
		{
			name: "flag tokens are ignored",
			diff: dffOnly("README.md"),
			commands: config.CommandsConfig{
				Test: "pytest -v --maxfail=1 tests/",
			},
			want: false,
		},
		{
			name:     "empty diff falls through to default true",
			diff:     "",
			commands: config.CommandsConfig{Test: "pytest tests/"},
			want:     true,
		},
		{
			name: "deletion-only diff still counts the file path",
			diff: "diff --git a/tests/old.py b/tests/old.py\n" +
				"deleted file mode 100644\n" +
				"--- a/tests/old.py\n" +
				"+++ /dev/null\n" +
				"@@ -1 +0,0 @@\n-old\n",
			commands: config.CommandsConfig{Test: "pytest tests/"},
			want:     true,
		},
		{
			name: "PR #135 reproducer — go ./... + docs-only diff → default true (heuristic does not help on this repo, but fallback intent path does)",
			diff: dffOnly("plans/PROGRESS.md", "plans/ROADMAP.md"),
			commands: config.CommandsConfig{
				Build: "make build",
				Test:  "make test",
				Lint:  "make lint",
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSourceDiff(tc.diff, tc.commands); got != tc.want {
				t.Errorf("IsSourceDiff = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestExtractSourcePrefixes(t *testing.T) {
	cases := []struct {
		name     string
		commands config.CommandsConfig
		want     []string
	}{
		{
			name:     "make-only commands have no derivable paths",
			commands: config.CommandsConfig{Build: "make build", Test: "make test", Lint: "make lint"},
			want:     nil,
		},
		{
			name:     "go ./... yields no concrete prefix",
			commands: config.CommandsConfig{Test: "go test ./..."},
			want:     nil,
		},
		{
			name:     "pytest tests/ yields tests/",
			commands: config.CommandsConfig{Test: "pytest tests/"},
			want:     []string{"tests/"},
		},
		{
			name:     "eslint src/**/*.ts yields src/",
			commands: config.CommandsConfig{Lint: "eslint src/**/*.ts"},
			want:     []string{"src/"},
		},
		{
			name:     "multiple path args",
			commands: config.CommandsConfig{Test: "go test ./internal/... ./cmd/..."},
			want:     []string{"internal/", "cmd/"},
		},
		{
			name:     "flag tokens skipped",
			commands: config.CommandsConfig{Test: "pytest -v --maxfail=1 tests/unit"},
			want:     []string{"tests/unit/"},
		},
		{
			name: "duplicate prefixes deduped",
			commands: config.CommandsConfig{
				Build: "go build ./internal/...",
				Test:  "go test ./internal/...",
			},
			want: []string{"internal/"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractSourcePrefixes(tc.commands)
			if !equalStrings(got, tc.want) {
				t.Errorf("extractSourcePrefixes = %v, want %v", got, tc.want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
