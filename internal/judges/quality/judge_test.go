package quality

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/judges/verdictschema"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

func TestQualitySystemPrompt_IncludesBaseline(t *testing.T) {
	// #84: the quality judge prompt must include the non-negotiable
	// standards so violations can be flagged with the baseline marker.
	if !strings.Contains(systemPrompt, standards.Block) {
		t.Error("quality judge system prompt must include the baseline standards block")
	}
	for _, tag := range standards.AllRules {
		if !strings.Contains(systemPrompt, string(tag)) {
			t.Errorf("quality judge system prompt missing baseline rule tag %q", tag)
		}
	}
}

func TestQualityJudge_BaselineMarkerForcesBlocking(t *testing.T) {
	// A P1 baseline gap is already blocking under the quality judge's
	// default block set (P0+P1), so this test primarily guards the wiring
	// — a regression where ForceBaselineBlocking stops being called would
	// not show up here, but the unit test in the standards package covers
	// the promotion logic itself. The observable behavior here is: the
	// gap must end up Blocking=true regardless of what the LLM said.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityHigh, Description: "BASELINE: no-secrets: hardcoded token", Blocking: false},
			},
		},
	}
	judge := New(fake, nil, testConfig())

	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("baseline-marked gap must be blocking")
	}
	if verdict.Pass {
		t.Error("pass must be false when a blocking baseline gap is present")
	}
}

// FakeRunner returns configurable output for testing e2e commands.
type FakeRunner struct {
	Responses map[string]fakeResponse
}

type fakeResponse struct {
	Output []byte
	Err    error
}

func (f *FakeRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	key := name
	for _, a := range args {
		key += " " + a
	}
	if resp, ok := f.Responses[key]; ok {
		return resp.Output, resp.Err
	}
	return nil, nil
}

func testConfig() config.Config {
	return config.Config{
		Project: config.ProjectConfig{Name: "test"},
		Phases: config.PhasesConfig{
			Quality: config.QualityPhaseConfig{
				MaxLoops:     3,
				E2ERequired:  false,
				PRReviewMode: "auto",
			},
		},
	}
}

func testConfigWithE2E() config.Config {
	cfg := testConfig()
	cfg.Phases.Quality.E2ERequired = true
	cfg.Commands.E2E = "make e2e"
	return cfg
}

func TestQualityJudge_VerdictStampedWithModel(t *testing.T) {
	// AC: verdict output records which model produced the verdict so PR
	// comments / logs can show which judge model graded the change.
	fake := &claude.FakeClient{
		ModelName: "claude-opus-4-7",
		Response:  state.Verdict{Gaps: []state.Gap{}},
	}
	judge := New(fake, nil, testConfig())

	verdict, err := judge.Judge(context.Background(), "intent", "plan", "diff --git a/x.go b/x.go\n+func H() {}", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Model != "claude-opus-4-7" {
		t.Errorf("verdict.Model = %q, want claude-opus-4-7", verdict.Model)
	}
}

func TestJudge_Pass_NoGapsScoresFull(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "build a REST API", "1. Create handlers\n2. Add routes", "diff --git a/x.go b/x.go\n+func H() {}", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true with 0 gaps")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100 with 0 gaps, got %f", verdict.Score)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].ToolName != verdictschema.ToolName {
		t.Errorf("expected tool name %q, got %q", verdictschema.ToolName, fake.Calls[0].ToolName)
	}
	if !contains(fake.Calls[0].Prompt, "build a REST API") {
		t.Error("expected prompt to contain intent")
	}
	if !contains(fake.Calls[0].Prompt, "Create handlers") {
		t.Error("expected prompt to contain plan")
	}
}

func TestJudge_IntentMismatch_P0Blocks(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityCritical, Description: "code implements CRUD but intent was auth system"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "build auth system", "1. Implement auth", "diff --git a/x.go b/x.go\n+func crud() {}", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Pass {
		t.Error("expected pass=false for blocking P0 gap")
	}
	// 100 - 40 = 60.
	if verdict.Score != 60 {
		t.Errorf("expected score 60 (100-40), got %f", verdict.Score)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("expected P0 gap to be blocking")
	}
}

func TestJudge_E2EPass(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("all tests passed")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when AI and e2e both pass")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100, got %f", verdict.Score)
	}
}

func TestJudge_E2EFail_AddsBlockingGap(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("FAIL: connection refused"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// E2E fail adds a P1 gap -> 100 - 20 = 80.
	if verdict.Score != 80 {
		t.Errorf("expected score 80, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false due to blocking e2e gap, even with score 80")
	}
	if len(verdict.Gaps) != 1 {
		t.Fatalf("expected 1 gap from e2e, got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityHigh {
		t.Errorf("expected P1 severity for e2e failure, got %s", verdict.Gaps[0].Severity)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("expected e2e failure gap to be blocking (assigned deterministically)")
	}
}

func TestJudge_E2ENotRequired(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	cfg := testConfig()
	cfg.Commands.E2E = "make e2e"
	// E2ERequired is false by default.

	judge := New(fake, nil, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100, got %f", verdict.Score)
	}
}

func TestJudge_E2ERequiredNoCommand(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	cfg := testConfig()
	cfg.Phases.Quality.E2ERequired = true
	// Commands.E2E is empty.

	judge := New(fake, nil, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when no e2e command configured")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100, got %f", verdict.Score)
	}
}

func TestJudge_AccumulatedP2sDragBelowThreshold(t *testing.T) {
	// Four P2 gaps -> 100 - 40 = 60, below PassThreshold (70).
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityMedium, Description: "a"},
				{Severity: state.SeverityMedium, Description: "b"},
				{Severity: state.SeverityMedium, Description: "c"},
				{Severity: state.SeverityMedium, Description: "d"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 60 {
		t.Errorf("expected score 60, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false when accumulated non-blocking gaps drag score below threshold")
	}
}

func TestJudge_PassAtExactThreshold(t *testing.T) {
	// Three P2 gaps -> 100 - 30 = 70, exactly equal to threshold. Non-blocking.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityMedium, Description: "a"},
				{Severity: state.SeverityMedium, Description: "b"},
				{Severity: state.SeverityMedium, Description: "c"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 70 {
		t.Errorf("expected score 70, got %f", verdict.Score)
	}
	if !verdict.Pass {
		t.Error("expected pass=true when score equals threshold exactly")
	}
}

func TestJudge_ClientError(t *testing.T) {
	fake := &claude.FakeClient{
		Err: fmt.Errorf("connection refused"),
	}

	judge := New(fake, nil, testConfig())
	_, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err == nil {
		t.Fatal("expected error when client fails")
	}
}

func TestJudge_MixedGapsWithE2E(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityMedium, Description: "missing edge case handling"},
			},
			Questions: []state.Question{
				{Text: "Should we add retry logic?", Priority: "medium"},
			},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("timeout"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One P2 (-10) + one P1 e2e (-20) = 100 - 30 = 70.
	if verdict.Score != 70 {
		t.Errorf("expected score 70, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false — the e2e P1 gap is blocking")
	}
	if len(verdict.Gaps) != 2 {
		t.Fatalf("expected 2 gaps (1 AI + 1 e2e), got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[1].Severity != state.SeverityHigh {
		t.Errorf("expected second gap P1 (e2e), got %s", verdict.Gaps[1].Severity)
	}
	if len(verdict.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(verdict.Questions))
	}
}

func TestJudge_ScoreFloorAtZero(t *testing.T) {
	// Many severe gaps -> penalty exceeds 100, score must floor at 0.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityCritical, Description: "wrong feature"},
				{Severity: state.SeverityCritical, Description: "missing core"},
				{Severity: state.SeverityCritical, Description: "broken api"},
			},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("FAIL"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 0 {
		t.Errorf("expected score 0 (floored), got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false")
	}
}

func TestJudge_PromptContainsDiff(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{Gaps: []state.Gap{}},
	}

	judge := New(fake, nil, testConfig())
	const diff = "diff --git a/foo.go b/foo.go\n+++ b/foo.go\n+func Foo() {}"
	_, err := judge.Judge(context.Background(), "intent", "plan", diff, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if !contains(fake.Calls[0].Prompt, "func Foo() {}") {
		t.Error("expected prompt to contain diff content")
	}
	if !contains(fake.Calls[0].Prompt, "## Diff") {
		t.Error("expected prompt to contain diff section header")
	}
}

func TestJudge_EmptyDiffPlaceholder(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{Gaps: []state.Gap{}},
	}

	judge := New(fake, nil, testConfig())
	_, err := judge.Judge(context.Background(), "intent", "plan", "", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if !contains(fake.Calls[0].Prompt, "no diff provided") {
		t.Error("expected empty-diff placeholder in prompt")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// docsOnlyMarker is a stable substring of the docs-only framing block
// the judge prepends when IsSourceDiff(diff) returns false. Tests
// assert presence/absence by this marker so wording can evolve without
// churn-y test diffs.
const docsOnlyMarker = "docs/scoping PR"

// TestJudge_DocsOnlyFraming_PrependedWhenDiffMissesSourcePaths is the
// regression test for issue #136 / PR #135. The judge must detect that
// a diff touches no paths matching commands.test/build/lint and tell
// the LLM to grade it as a docs/scoping PR rather than against
// arbitrary code criteria.
func TestJudge_DocsOnlyFraming_PrependedWhenDiffMissesSourcePaths(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{}}
	cfg := testConfig()
	cfg.Commands.Test = "pytest tests/"
	judge := New(fake, nil, cfg)

	diff := dffOnly("plans/PROGRESS.md", "plans/ROADMAP.md")
	if _, err := judge.Judge(context.Background(), "intent", "plan", diff, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 completer call, got %d", len(fake.Calls))
	}
	prompt := fake.Calls[0].Prompt
	if !strings.Contains(prompt, docsOnlyMarker) {
		t.Errorf("expected docs-only framing in prompt; missing marker %q.\nprompt:\n%s",
			docsOnlyMarker, prompt)
	}
}

// TestJudge_DocsOnlyFraming_AbsentWhenDiffTouchesSourcePaths is the
// inverse — when the diff lands inside a configured source path, the
// docs-only framing must NOT appear, otherwise the judge will go soft
// on legitimate code PRs.
func TestJudge_DocsOnlyFraming_AbsentWhenDiffTouchesSourcePaths(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{}}
	cfg := testConfig()
	cfg.Commands.Test = "pytest tests/"
	judge := New(fake, nil, cfg)

	diff := dffOnly("tests/test_foo.py")
	if _, err := judge.Judge(context.Background(), "intent", "plan", diff, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prompt := fake.Calls[0].Prompt
	if strings.Contains(prompt, docsOnlyMarker) {
		t.Errorf("docs-only framing leaked into a source-touching diff.\nprompt:\n%s", prompt)
	}
}

// TestJudge_DocsOnlyFraming_AbsentWhenNoSourcePrefixesDerivable
// guards the conservative default: when commands have no derivable
// source paths (e.g. `make build`), IsSourceDiff falls back to true
// and the framing must NOT fire.
func TestJudge_DocsOnlyFraming_AbsentWhenNoSourcePrefixesDerivable(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{}}
	cfg := testConfig()
	cfg.Commands.Build = "make build"
	cfg.Commands.Test = "make test"
	cfg.Commands.Lint = "make lint"
	judge := New(fake, nil, cfg)

	diff := dffOnly("plans/PROGRESS.md")
	if _, err := judge.Judge(context.Background(), "intent", "plan", diff, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	prompt := fake.Calls[0].Prompt
	if strings.Contains(prompt, docsOnlyMarker) {
		t.Errorf("docs-only framing fired when no source prefixes are derivable.\nprompt:\n%s", prompt)
	}
}

func TestJudge_SummaryRoundTrip(t *testing.T) {
	want := "## Reviewed\n- Intent matches implementation\n\n## Notes\n- e2e tests still green"
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Summary: want,
		},
	}

	cfg := config.Config{}
	cfg.Phases.Quality.E2ERequired = false
	judge := New(fake, &FakeRunner{}, cfg)
	verdict, err := judge.Judge(context.Background(), "build it", "the plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Summary != want {
		t.Errorf("summary lost in round-trip\n got: %q\nwant: %q", verdict.Summary, want)
	}
}

func TestJudge_SystemPromptMentionsSummary(t *testing.T) {
	if !strings.Contains(systemPrompt, "summary") {
		t.Error("system prompt no longer instructs the LLM to emit a summary field")
	}
	for _, section := range []string{"## Reviewed", "## Notes"} {
		if !strings.Contains(systemPrompt, section) {
			t.Errorf("system prompt missing summary sub-section %q", section)
		}
	}
}

func TestJudge_SystemPromptContainsSecurityChecks(t *testing.T) {
	for _, keyword := range []string{
		"### Security",
		"Hardcoded secrets",
		"SQL injection",
		"Command injection",
		"Path traversal",
		"insecure crypto",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing security keyword %q", keyword)
		}
	}
}

func TestJudge_SystemPromptContainsCodeReuseChecks(t *testing.T) {
	for _, keyword := range []string{
		"### Code reuse",
		"duplicated",
		"copy-pasted",
		"near-identical",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing code-reuse keyword %q", keyword)
		}
	}
}

func TestJudge_SystemPromptContainsStyleChecks(t *testing.T) {
	for _, keyword := range []string{
		"### Style",
		"maintainability",
		"Magic numbers",
		"nested control flow",
		"error handling",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing style keyword %q", keyword)
		}
	}
}

func TestJudge_SecurityChecksAreBlocking(t *testing.T) {
	if !strings.Contains(systemPrompt, "high — blocking") {
		t.Error("system prompt should mark security checks as high — blocking")
	}
}

func TestJudge_SystemPromptForbidsSilenceOnSubstantiveDiff(t *testing.T) {
	// PR #107 was a 1200-line / 16-file diff that the judge passed with
	// zero gaps. The prompt must keep an explicit guard against that
	// failure mode — but via a severity-ordered scan, not a count anchor
	// (which earlier wording produced "always 3 P3 nits" false positives).
	for _, keyword := range []string{
		"Substantive-diff rule",
		">200 lines",
		">3 files",
		"severity-ordered scan",
		"without performing the scan is a failure mode",
		// Example 5 demonstrates the empty-gaps-on-substantive-diff
		// outcome. Pin it so a future edit cannot quietly delete the
		// concrete training signal while leaving the abstract rule.
		"Example 5",
		"severity scan surfaced no concerns",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing substantive-diff rule marker %q", keyword)
		}
	}

	// Guard against re-introducing count anchors. These phrases were
	// the root cause of the "judge always emits ~3 soft P3 gaps"
	// regression that this rewrite fixes.
	for _, banned := range []string{
		"typically 2–3",
		"typically 2-3",
		"MUST produce at least one entry",
		"expected floor",
	} {
		if strings.Contains(systemPrompt, banned) {
			t.Errorf("system prompt contains banned count-anchor phrase %q — use severity-ordered scan instead", banned)
		}
	}
}

func TestJudge_SystemPromptRequiresReadingTheWholeHunk(t *testing.T) {
	// PR #140 was a 491-line / 15-file diff where the judge posted three
	// inline P2/P3 gaps asking for doc comments at lines that already had
	// doc comment blocks immediately above them. The judge anchored to a
	// single line without reading the surrounding hunk. The prompt must
	// explicitly require checking adjacent context before flagging a
	// "missing X" gap.
	for _, keyword := range []string{
		"Read the whole hunk",
		"missing doc comment",
		"already exists in",
		// A soft window keeps the rule actionable on large hunks
		// without asking the judge to re-read 500-line diffs.
		"30 lines",
		// Example 6 is the concrete demonstration; pin it so future
		// edits cannot remove the worked example while leaving only
		// the abstract rule.
		"Example 6",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing whole-hunk-reading marker %q", keyword)
		}
	}
}

func TestJudge_SystemPromptCoversCrossFileConsistency(t *testing.T) {
	// PR #140 also missed a duplicated --model arg pattern across two
	// methods in the claudecli client (drift risk). The "Additional
	// checks" section must cover the same-pattern-applied-in-multiple-
	// places case so future reviews catch divergence between sites.
	// Severity must follow impact: cosmetic drift is P2, but divergence
	// that produces incorrect behaviour at one site is P1, not P2.
	for _, keyword := range []string{
		"Cross-file consistency",
		"drift risk",
		"severity follows impact",
		"correctness bug, not a style issue",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing cross-file-consistency marker %q", keyword)
		}
	}
}

func TestJudge_SystemPromptEstablishesFreshReviewerMindset(t *testing.T) {
	// When judge and coder are backed by the same model family, the judge
	// can carry an implicit self-defense bias. The prompt must instruct
	// it to review as a fresh reviewer with no prior reasoning about the
	// change.
	for _, keyword := range []string{
		"Reviewer mindset",
		"no prior",
		"NOT the author",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing fresh-reviewer marker %q", keyword)
		}
	}
}

func TestJudge_CodeReuseAndStyleAreNonBlocking(t *testing.T) {
	if !strings.Contains(systemPrompt, "medium — non-blocking") {
		t.Error("system prompt should mark code-reuse checks as medium — non-blocking")
	}
	if !strings.Contains(systemPrompt, "low — non-blocking") {
		t.Error("system prompt should mark style checks as low — non-blocking")
	}
}

func TestJudge_SystemPromptRequestsFileAndLine(t *testing.T) {
	// #72: the system prompt must instruct the LLM to include file/line
	// in gaps so inline PR comments can be posted. #100 follow-up: the
	// prompt must also push back against omitting file/line — otherwise
	// the judge tends to classify everything as "architectural" and
	// gaps fall out of the inline-review path.
	for _, keyword := range []string{
		`"file"`,
		`"line"`,
		"b/ side",
		"+ side",
		"ANY plausible anchor",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing file/line keyword %q", keyword)
		}
	}
}

func TestJudge_GapWithFileAndLine(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{
					Severity:    state.SeverityMedium,
					Description: "magic number",
					File:        "internal/foo/bar.go",
					Line:        42,
				},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(verdict.Gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[0].File != "internal/foo/bar.go" {
		t.Errorf("expected file = %q, got %q", "internal/foo/bar.go", verdict.Gaps[0].File)
	}
	if verdict.Gaps[0].Line != 42 {
		t.Errorf("expected line = 42, got %d", verdict.Gaps[0].Line)
	}
}

func TestJudge_CodeFactsInjectedIntoPrompt(t *testing.T) {
	// Issue #85: objective checks (tests pass, lint clean, build succeeds) must
	// be sourced from the code judge and injected as facts, so the LLM does
	// not re-evaluate them.
	fake := &claude.FakeClient{
		Response: state.Verdict{Gaps: []state.Gap{}},
	}

	judge := New(fake, nil, testConfig()).WithCodeFacts("Score: 100%\nAll checks passed (lint, test, build)")
	_, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(fake.Calls[0].Prompt, "## Facts (from code judge)") {
		t.Error("expected facts section in prompt")
	}
	if !contains(fake.Calls[0].Prompt, "All checks passed") {
		t.Error("expected fact body in prompt")
	}
}

func TestJudge_SystemPromptInstructsNoRecheckOfObjectiveChecks(t *testing.T) {
	// The judge must tell the LLM to trust the code judge's results.
	for _, keyword := range []string{
		"tests",
		"lint",
		"build",
		"code judge",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing 'do not re-evaluate' keyword %q", keyword)
		}
	}
}

func TestJudge_SystemPromptIncludesFewShotExamples(t *testing.T) {
	// Issue #85 requires at least 2 few-shot examples (one pass, one fail).
	// Post-#79 we add Example 3 — a false-positive the judge must NOT
	// produce — so keep all three anchors asserted.
	for _, needle := range []string{"Example 1", "Example 2", "Example 3", "submit_verdict"} {
		if !strings.Contains(systemPrompt, needle) {
			t.Errorf("system prompt missing few-shot anchor %q", needle)
		}
	}
}

func TestJudge_SystemPromptHasPartialDiffFalsePositiveExample(t *testing.T) {
	// The judge violated the partial-diff rule on PR #99 by flagging an
	// existing same-package function as "undefined". Keep a concrete
	// false-positive example + its correction in the prompt so the model
	// sees the mistake pattern explicitly, not just the abstract rule.
	for _, needle := range []string{
		"INCORRECT submit_verdict",
		"CORRECT submit_verdict",
		"same-package symbols do not need imports",
	} {
		if !strings.Contains(systemPrompt, needle) {
			t.Errorf("system prompt missing partial-diff false-positive anchor %q", needle)
		}
	}
}

func TestJudge_BlockingGapFailsEvenWithHighScore(t *testing.T) {
	// A single P1 gap costs only 20 points (score stays at 80), but the
	// gap is blocking, so the verdict must still fail.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityHigh, Description: "tautological assertion"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 80 {
		t.Errorf("expected score 80, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false — blocking gap")
	}
}

func TestJudge_NonBlockingGapsAllowPass(t *testing.T) {
	// One P2 + one P3 -> 100 - 10 - 5 = 85, non-blocking, pass.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityMedium, Description: "magic number"},
				{Severity: state.SeverityLow, Description: "long function"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 85 {
		t.Errorf("expected score 85, got %f", verdict.Score)
	}
	if !verdict.Pass {
		t.Error("expected pass=true — only non-blocking gaps, score above threshold")
	}
}

func TestCheckPathHandler_ExistingDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/sub/dir", 0o755); err != nil {
		t.Fatal(err)
	}
	// chdir so the handler resolves relative to our temp dir.
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	_ = os.Chdir(dir)

	result, err := checkPathHandler(context.Background(), []byte(`{"path":"sub/dir"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "exists: true, type: directory" {
		t.Errorf("expected directory exists, got %q", result)
	}
}

func TestCheckPathHandler_ExistingFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/test.txt", []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	_ = os.Chdir(dir)

	result, err := checkPathHandler(context.Background(), []byte(`{"path":"test.txt"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "exists: true, type: file" {
		t.Errorf("expected file exists, got %q", result)
	}
}

func TestCheckPathHandler_NonExistent(t *testing.T) {
	result, err := checkPathHandler(context.Background(), []byte(`{"path":"does/not/exist"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "exists: false" {
		t.Errorf("expected not exists, got %q", result)
	}
}

func TestCheckPathHandler_PathTraversal(t *testing.T) {
	result, err := checkPathHandler(context.Background(), []byte(`{"path":"../../../etc/passwd"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "error") {
		t.Errorf("expected error for path traversal, got %q", result)
	}
}

func TestCheckPathHandler_AbsolutePath(t *testing.T) {
	result, err := checkPathHandler(context.Background(), []byte(`{"path":"/etc/passwd"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(result, "error") {
		t.Errorf("expected error for absolute path, got %q", result)
	}
}

func TestJudge_SystemPromptMentionsCheckPath(t *testing.T) {
	if !strings.Contains(systemPrompt, "check_path") {
		t.Error("system prompt must mention the check_path tool")
	}
}

func TestJudge_ReturnTo_ClearedOnPass(t *testing.T) {
	// Even if the LLM erroneously emits a ReturnTo on a passing verdict
	// (e.g. leftover from a previous response), the judge must clear it
	// — a passing verdict never rewinds.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps:     []state.Gap{},
			ReturnTo: state.ReturnToCode,
		},
	}
	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Pass {
		t.Fatal("expected pass")
	}
	if verdict.ReturnTo != state.ReturnToNone {
		t.Errorf("ReturnTo must be cleared on passing verdict, got %q", verdict.ReturnTo)
	}
}

func TestJudge_ReturnTo_Propagated(t *testing.T) {
	cases := []struct {
		name string
		in   state.ReturnTo
	}{
		{"code", state.ReturnToCode},
		{"plan", state.ReturnToPlan},
		{"escalate", state.ReturnToEscalate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &claude.FakeClient{
				Response: state.Verdict{
					Gaps: []state.Gap{
						{Severity: state.SeverityCritical, Description: "failing"},
					},
					ReturnTo: tc.in,
				},
			}
			judge := New(fake, nil, testConfig())
			verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if verdict.Pass {
				t.Error("expected fail with P0 gap")
			}
			if verdict.ReturnTo != tc.in {
				t.Errorf("ReturnTo = %q, want %q", verdict.ReturnTo, tc.in)
			}
		})
	}
}

func TestJudge_ReturnTo_DefaultsToCodeOnBlockingFailure(t *testing.T) {
	// The LLM may forget to emit ReturnTo. For a blocking failure we
	// default to ReturnToCode — the pre-#87 heuristic was to route every
	// P0/P1 blocking failure back to code, so that's the safest default.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityHigh, Description: "bug"},
			},
			// ReturnTo deliberately empty.
		},
	}
	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail with blocking P1 gap")
	}
	if verdict.ReturnTo != state.ReturnToCode {
		t.Errorf("ReturnTo default on blocking failure = %q, want code", verdict.ReturnTo)
	}
}

func TestJudge_ReturnTo_EmptyForNonBlockingFailure(t *testing.T) {
	// Non-blocking-but-failing verdict (score dragged below threshold
	// by P2s) should not request a cross-phase rewind — the quality
	// phase can retry in place.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityMedium, Description: "a"},
				{Severity: state.SeverityMedium, Description: "b"},
				{Severity: state.SeverityMedium, Description: "c"},
				{Severity: state.SeverityMedium, Description: "d"},
			},
		},
	}
	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail with accumulated P2s")
	}
	if verdict.ReturnTo != state.ReturnToNone {
		t.Errorf("ReturnTo on non-blocking failure should be empty, got %q", verdict.ReturnTo)
	}
}

func TestJudge_ReturnTo_UnknownValueCollapsesToCode(t *testing.T) {
	// Defensive normalisation: if the LLM emits a value outside the
	// enum (e.g. a typo), treat it as the safe default for a blocking
	// failure rather than passing it through and surprising the
	// orchestrator with an unhandled route.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityCritical, Description: "bug"},
			},
			ReturnTo: state.ReturnTo("rewrite"),
		},
	}
	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.ReturnTo != state.ReturnToCode {
		t.Errorf("unknown ReturnTo should collapse to code, got %q", verdict.ReturnTo)
	}
}

func TestJudge_SystemPromptExplainsReturnTo(t *testing.T) {
	// The prompt must instruct the LLM to diagnose the root cause and
	// emit ReturnTo — otherwise the whole cross-phase rewind machinery
	// cannot kick in.
	for _, needle := range []string{
		"return_to",
		"\"code\"",
		"\"plan\"",
		"\"escalate\"",
		"root cause",
	} {
		if !strings.Contains(systemPrompt, needle) {
			t.Errorf("system prompt missing ReturnTo instruction %q", needle)
		}
	}
}

func TestJudge_UsesCompleteWithTools(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{Gaps: []state.Gap{}},
	}

	judge := New(fake, nil, testConfig())
	_, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].ToolName != verdictschema.ToolName {
		t.Errorf("expected final tool %q, got %q", verdictschema.ToolName, fake.Calls[0].ToolName)
	}
}

// TestJudge_SystemPromptHasBeforeFlaggingRubric pins the new
// rubric: before emitting a Medium / Low / Standards gap, the judge
// must search the surrounding file for an existing handler / guard /
// convention that already addresses the concern. If found, drop the
// gap. This is a generalization of the whole-hunk-reading rule
// (already covered) extended explicitly to the lower severities and
// to Standards findings — exactly the tiers most likely to be false
// positives, since by definition they don't change correctness.
func TestJudge_SystemPromptHasBeforeFlaggingRubric(t *testing.T) {
	for _, keyword := range []string{
		"Before flagging",
		"Medium",
		"Low",
		"Standards",
		"existing handler",
		"do not flag",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing before-flagging-rubric marker %q", keyword)
		}
	}
}

// TestJudge_SystemPromptHasCountAnchorScan goes red if any phrasing
// that anchors the gap count to a specific number creeps back into
// the prompt. PR #141 removed "typically 2-3 P3/P2 design observations";
// PR #145 added Medium/Low/Standards to the dispatch table where
// padding nits are most expensive. This guard pins the absence of
// the regression.
func TestJudge_SystemPromptHasNoCountAnchor(t *testing.T) {
	for _, banned := range []string{
		"typically 2",
		"typically 3",
		"at least 2",
		"at least 3",
		"2-3 P",
		"2-3 gaps",
		"2 to 3",
		"up to 3 gaps",
	} {
		if strings.Contains(systemPrompt, banned) {
			t.Errorf("system prompt re-introduced count-anchor phrase %q — see PR #141", banned)
		}
	}
}

// TestRenderCrossPushFraming produces the cross-push framing the
// quality judge prepends to the user prompt when prior verdict gaps
// exist. The framing tells the judge:
//
//   - the prior review's gap list (with severities)
//   - to verify each prior gap is still applicable in the current diff
//     and drop it if fixed
//   - to scan only the diff since the prior review for new findings
//   - NOT to introduce findings that existed before the prior review
//     (if the previous round missed them, they're not flagged now)
//
// Empty prior-gap list returns the empty string so the framing
// disappears on the first review of a PR.
func TestRenderCrossPushFraming_EmptyOnFirstReview(t *testing.T) {
	if got := RenderCrossPushFraming(nil); got != "" {
		t.Errorf("RenderCrossPushFraming(nil) = %q, want empty", got)
	}
	if got := RenderCrossPushFraming([]state.Gap{}); got != "" {
		t.Errorf("RenderCrossPushFraming(empty) = %q, want empty", got)
	}
}

func TestRenderCrossPushFraming_IncludesPriorGapsAndInstructions(t *testing.T) {
	prior := []state.Gap{
		{Severity: state.SeverityCritical, Description: "missing auth on /admin"},
		{Severity: state.SeverityHigh, Description: "tests fail"},
	}
	got := RenderCrossPushFraming(prior)

	for _, want := range []string{
		"prior review",
		"missing auth on /admin",
		"tests fail",
		"Critical",
		"High",
		"Verify each",
		"drop it",
		"new findings",
		"Do not introduce",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderCrossPushFraming missing %q\n--- got ---\n%s", want, got)
		}
	}
}

// TestJudge_PrependsCrossPushFramingWhenPriorGapsPresent encodes the
// orchestrator wiring: when the caller passes a non-empty priorGaps
// slice, the judge prepends the cross-push framing block to the user
// prompt so the model sees both the framing AND the diff in the same
// turn. Without this, the helper added in commit 5 would never reach
// the model — the prompt would still look identical to a first-review
// prompt and the nagging-comment failure mode would persist.
func TestJudge_PrependsCrossPushFramingWhenPriorGapsPresent(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{Gaps: []state.Gap{}}}
	judge := New(fake, nil, testConfig())

	prior := []state.Gap{
		{Severity: state.SeverityCritical, Description: "auth missing on /admin"},
	}
	if _, err := judge.Judge(context.Background(), "intent", "plan", "diff", prior, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	prompt := fake.Calls[0].Prompt
	if !strings.Contains(prompt, "Cross-push awareness") {
		t.Errorf("prompt missing cross-push framing header\n--- prompt ---\n%s", prompt)
	}
	if !strings.Contains(prompt, "auth missing on /admin") {
		t.Errorf("prompt missing the prior gap description\n--- prompt ---\n%s", prompt)
	}
	// The original sections still come through after the framing.
	if !strings.Contains(prompt, "## Original Intent") {
		t.Error("prompt missing original intent section after framing")
	}
}

// TestJudge_OmitsCrossPushFramingOnFirstReview — first review of a PR
// (no prior gaps) must NOT include the framing, otherwise an empty
// "you reviewed this before" block would mislead the model.
func TestJudge_OmitsCrossPushFramingOnFirstReview(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{Gaps: []state.Gap{}}}
	judge := New(fake, nil, testConfig())

	if _, err := judge.Judge(context.Background(), "intent", "plan", "diff", nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if strings.Contains(fake.Calls[0].Prompt, "Cross-push awareness") {
		t.Error("first-review prompt should not contain cross-push framing")
	}
}

// --- AC-tracing tests (#126 / judge AC enforcement) ---
//
// These tests pin the contract that gives a quality judge teeth
// against literally-skipped acceptance criteria. With an AC list
// from the issue body, the judge MUST: (a) include every item by
// name in the prompt, (b) merge the model's per-item audit response
// into a final Checklist, (c) gate Pass on every Required item being
// either Passed or unpassed-with-reason, (d) surface a Critical
// Blocking gap for every unpassed-no-reason item so the verdict
// comment explains the failure.

// TestJudge_AC_PromptIncludesItems: the AC list must reach the
// model under a clearly labelled "## Acceptance Criteria" section
// with stable item names. Without this the model has no affordance
// to populate the checklist field of submit_verdict.
func TestJudge_AC_PromptIncludesItems(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{}}
	judge := New(fake, nil, testConfig())
	checklist := []state.ChecklistItem{
		{Name: "ac_1", Description: "Add codex completer", Required: true},
		{Name: "ac_2", Description: "Wire into resolver", Required: true},
	}
	if _, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, checklist); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prompt := fake.Calls[0].Prompt
	if !strings.Contains(prompt, "## Acceptance Criteria") {
		t.Errorf("prompt missing AC section header\n%s", prompt)
	}
	for _, want := range []string{"ac_1", "ac_2", "Add codex completer", "Wire into resolver"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q\n%s", want, prompt)
		}
	}
	// Negative-space instruction must reach the model — that's the
	// load-bearing instruction for catching "looked plausible, not
	// actually done" misses.
	if !strings.Contains(prompt, "evidence would I expect to see") {
		t.Errorf("prompt missing negative-space instruction\n%s", prompt)
	}
}

// TestJudge_AC_AllPassedYieldsPass: every required item passed with
// evidence → verdict.Pass=true, verdict.Checklist round-trips with
// merged Description/Required from source and Passed/Reason from
// the model's audit.
func TestJudge_AC_AllPassedYieldsPass(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Passed: true, Reason: "internal/foo.go:42"},
			{Name: "ac_2", Passed: true, Reason: "internal/bar.go:7"},
		},
	}}
	judge := New(fake, nil, testConfig())
	checklist := []state.ChecklistItem{
		{Name: "ac_1", Description: "first", Required: true},
		{Name: "ac_2", Description: "second", Required: true},
	}
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, checklist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Pass {
		t.Errorf("verdict.Pass = false, want true; gaps: %+v checklist: %+v", verdict.Gaps, verdict.Checklist)
	}
	if len(verdict.Checklist) != 2 {
		t.Fatalf("verdict.Checklist len = %d, want 2", len(verdict.Checklist))
	}
	// Description must come from source, evidence from audit.
	if verdict.Checklist[0].Description != "first" {
		t.Errorf("verdict.Checklist[0].Description = %q, want first", verdict.Checklist[0].Description)
	}
	if verdict.Checklist[0].Reason != "internal/foo.go:42" {
		t.Errorf("verdict.Checklist[0].Reason = %q, want evidence", verdict.Checklist[0].Reason)
	}
}

// TestJudge_AC_UnmetWithoutReasonBlocks: an item the model leaves
// unpassed with empty reason MUST: (a) flip Pass to false, (b)
// produce a Critical Blocking gap citing the unsatisfied criterion
// so the verdict comment explains why it failed instead of just
// emitting NEEDS_WORK.
func TestJudge_AC_UnmetWithoutReasonBlocks(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Passed: true, Reason: "internal/foo.go:1"},
			{Name: "ac_2", Passed: false}, // no reason — unjustified skip
		},
	}}
	judge := New(fake, nil, testConfig())
	checklist := []state.ChecklistItem{
		{Name: "ac_1", Description: "first", Required: true},
		{Name: "ac_2", Description: "wire codex into resolver", Required: true},
	}
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, checklist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Errorf("verdict.Pass = true, want false (ac_2 unmet without reason)")
	}
	// A blocking critical gap must surface the unsatisfied criterion.
	var found bool
	for _, g := range verdict.Gaps {
		if g.Severity == state.SeverityCritical && g.Blocking && strings.Contains(g.Description, "wire codex into resolver") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected a Critical Blocking gap citing the unmet AC item\ngaps: %+v", verdict.Gaps)
	}
}

// TestJudge_AC_DeferredWithReasonPasses: an unpassed Required item
// with a non-empty reason is acceptable — the deferral is recorded
// in the verdict but does not block. This is the "be honest about
// what's not done" path.
func TestJudge_AC_DeferredWithReasonPasses(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Passed: true, Reason: "internal/foo.go:1"},
			{Name: "ac_2", Passed: false, Reason: "deferred: depends on #130 (registry)"},
		},
	}}
	judge := New(fake, nil, testConfig())
	checklist := []state.ChecklistItem{
		{Name: "ac_1", Description: "first", Required: true},
		{Name: "ac_2", Description: "register with registry", Required: true},
	}
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, checklist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Pass {
		t.Errorf("verdict.Pass = false, want true (ac_2 deferred with reason)")
	}
	// No critical blocking gap should be added for the deferred item.
	for _, g := range verdict.Gaps {
		if g.Severity == state.SeverityCritical && strings.Contains(g.Description, "register with registry") {
			t.Errorf("deferred-with-reason item must not produce a blocking gap: %+v", g)
		}
	}
	// The deferral reason survives in verdict.Checklist.
	if verdict.Checklist[1].Reason != "deferred: depends on #130 (registry)" {
		t.Errorf("verdict.Checklist[1].Reason = %q, want the deferral note", verdict.Checklist[1].Reason)
	}
}

// TestJudge_AC_MissingAuditEntryBlocks: if the model fails to emit
// an audit entry for an AC item at all, the merge keeps Passed=false
// from the source and empty Reason — the gate must block. The judge
// cannot quietly skip an item by omission.
func TestJudge_AC_MissingAuditEntryBlocks(t *testing.T) {
	fake := &claude.FakeClient{Response: state.Verdict{
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Passed: true, Reason: "evidence"},
			// ac_2 omitted by the model
		},
	}}
	judge := New(fake, nil, testConfig())
	checklist := []state.ChecklistItem{
		{Name: "ac_1", Description: "first", Required: true},
		{Name: "ac_2", Description: "second", Required: true},
	}
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, checklist)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("verdict.Pass must be false when an AC has no audit entry")
	}
	// Sanity: the renderer-friendly gap must be present.
	var found bool
	for _, g := range verdict.Gaps {
		if g.Blocking && strings.Contains(g.Description, "second") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a blocking gap explaining the missing AC item")
	}
}

// TestJudge_AC_EmptyChecklistPreservesLegacyBehaviour: a task with
// no AC list keeps the threshold-based legacy pass — verdict.Pass
// reflects Score >= PassThreshold && !blocking. This guards
// backwards compatibility for tasks that don't have a parsed
// checklist (intents without `- [ ]` items).
func TestJudge_AC_EmptyChecklistPreservesLegacyBehaviour(t *testing.T) {
	// Score will be 100 (no gaps), no blocking — legacy gate passes.
	fake := &claude.FakeClient{Response: state.Verdict{}}
	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Pass {
		t.Error("legacy-mode verdict with no gaps should pass")
	}
	if len(verdict.Checklist) != 0 {
		t.Errorf("legacy-mode verdict should have no Checklist, got %d items", len(verdict.Checklist))
	}
	// Prompt must NOT include the AC section when no checklist was
	// supplied — otherwise we waste tokens on an empty section.
	if strings.Contains(fake.Calls[0].Prompt, "## Acceptance Criteria") {
		t.Error("prompt should not include AC section when checklist is empty")
	}
}
