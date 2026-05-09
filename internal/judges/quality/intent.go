package quality

import (
	"log/slog"
	"strings"

	"github.com/vairdict/vairdict/internal/config"
)

// IsSourceDiff reports whether a unified diff touches any path that the
// repo's configured build/test/lint commands would treat as source.
//
// vairdict has to grade docs-only PRs differently from code PRs (issue
// #136), but it cannot hardcode "is `.go`?" — vairdict is meant to run
// on Python, JS, Rust, … repos too. The commands.build/test/lint values
// in vairdict.yaml already describe what each repo treats as source
// (e.g. `go test ./internal/...`, `pytest tests/`, `eslint src/`). This
// function extracts path tokens from those commands and checks whether
// any diff file falls under one.
//
// When no source prefixes can be derived (e.g. `make test`, `npm test`,
// empty commands, Go's `./...` wildcard) the heuristic returns true and
// emits an `slog.Info` line — conservative on purpose. Skipping a real
// code change because we couldn't infer a path would be a worse
// failure than over-reviewing a docs PR; the closing-keyword tightening
// in `internal/github.linkedIssueRe` is the primary defence against the
// PR #135 false-blocker, with this heuristic as a secondary signal for
// repos that DO ship explicit source paths in their commands.
func IsSourceDiff(diff string, commands config.CommandsConfig) bool {
	if strings.TrimSpace(diff) == "" {
		return true
	}
	prefixes := extractSourcePrefixes(commands)
	if len(prefixes) == 0 {
		slog.Info("no source paths derivable from commands; treating diff as code by default",
			"build", commands.Build,
			"test", commands.Test,
			"lint", commands.Lint,
		)
		return true
	}
	paths := extractDiffPaths(diff)
	for _, p := range paths {
		for _, prefix := range prefixes {
			if pathHasPrefix(p, prefix) {
				return true
			}
		}
	}
	return false
}

// extractSourcePrefixes pulls path-like tokens out of the
// build/test/lint command strings and returns them as directory
// prefixes (always trailing-slash-terminated). Tokens that are not
// paths — flags, bare command names like `go`/`make`/`pytest`, the Go
// wildcard `./...` — are skipped.
//
// A token is path-like when it contains at least one `/` AND, after
// stripping a leading `./` and trailing wildcards (`/**`, `/*`,
// `/...`), it still has a non-empty directory part. That last check
// rejects `./...` (becomes empty) without rejecting `./internal/...`
// (becomes `internal/`).
func extractSourcePrefixes(commands config.CommandsConfig) []string {
	var out []string
	seen := map[string]bool{}
	for _, cmd := range []string{commands.Build, commands.Test, commands.Lint, commands.E2E} {
		for _, tok := range strings.Fields(cmd) {
			prefix, ok := pathPrefix(tok)
			if !ok {
				continue
			}
			if seen[prefix] {
				continue
			}
			seen[prefix] = true
			out = append(out, prefix)
		}
	}
	return out
}

// pathPrefix returns the directory prefix of a command-line token, or
// ("", false) if the token isn't a path. The returned prefix always
// ends in `/` so prefix matches are aligned to directory boundaries.
func pathPrefix(tok string) (string, bool) {
	if strings.HasPrefix(tok, "-") {
		return "", false
	}
	if !strings.Contains(tok, "/") {
		return "", false
	}
	tok = strings.TrimPrefix(tok, "./")
	// Cut at the first wildcard segment — `**`, `...`, `*`, `*.ext`.
	parts := strings.Split(tok, "/")
	dirs := parts[:0]
	for _, p := range parts {
		if p == "" || p == "..." || p == "**" || strings.Contains(p, "*") {
			break
		}
		dirs = append(dirs, p)
	}
	if len(dirs) == 0 {
		return "", false
	}
	// If the original token had a trailing slash and the last dir is
	// the only segment, we already kept it. Either way, return the
	// joined dirs with a trailing `/`.
	return strings.Join(dirs, "/") + "/", true
}

// pathHasPrefix reports whether p sits under prefix (which is always
// terminated with `/`). Equality is allowed: a diff path equal to a
// prefix-stripped directory still counts as "under" it.
func pathHasPrefix(p, prefix string) bool {
	if prefix == "" {
		return true
	}
	return strings.HasPrefix(p, prefix)
}

// renderDocsOnlyFraming returns the framing block prepended to the
// user prompt when IsSourceDiff has determined the diff touches no
// configured source paths. The marker phrase "docs/scoping PR" is
// asserted on by tests, so wording can evolve as long as the marker
// stays.
func renderDocsOnlyFraming() string {
	return "## Docs-only PR — adjust expectations\n\n" +
		"This PR touches no paths that the repo's commands.build/test/lint\n" +
		"configuration treats as source. Treat it as a docs/scoping PR.\n" +
		"Do not raise critical or high gaps for missing code, missing\n" +
		"tests, or unimplemented acceptance criteria unless the intent\n" +
		"text explicitly requires code changes — a docs-only diff is the\n" +
		"correct shape for a docs-only intent. Style / clarity / accuracy\n" +
		"observations on the docs themselves are still in scope.\n\n"
}

// extractDiffPaths walks a unified diff and returns every file path
// referenced in `+++ b/<path>` or `--- a/<path>` headers. Both sides
// are scanned so that pure deletions (where `+++` points at
// `/dev/null`) still contribute their path.
func extractDiffPaths(diff string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		var path string
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			path = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "--- a/"):
			path = strings.TrimPrefix(line, "--- a/")
		default:
			continue
		}
		path = strings.TrimSpace(path)
		if path == "" || path == "/dev/null" {
			continue
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}
