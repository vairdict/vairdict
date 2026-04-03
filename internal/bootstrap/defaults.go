package bootstrap

import "github.com/vairdict/vairdict/internal/config"

// DefaultsForType returns a Config with sensible defaults tuned for
// the given project type. Unknown types fall back to "startup" defaults.
func DefaultsForType(projectType string) config.Config {
	base := config.Defaults()

	switch projectType {
	case "startup":
		base.Phases.Plan.CoverageThreshold = 70
		base.Phases.Plan.Severity.BlockOn = []string{"P0"}
		base.Phases.Plan.Severity.AssumeOn = []string{"P1", "P2"}
		base.Phases.Plan.Severity.DeferOn = []string{"P3"}
		base.Phases.Code.CoverageMinimum = 60
		base.Phases.Quality.E2ERequired = false
		base.Phases.Quality.PRReviewMode = "auto"

	case "enterprise":
		base.Phases.Plan.CoverageThreshold = 85
		base.Phases.Plan.Severity.BlockOn = []string{"P0", "P1"}
		base.Phases.Plan.Severity.AssumeOn = []string{"P2"}
		base.Phases.Plan.Severity.DeferOn = []string{"P3"}
		base.Phases.Code.CoverageMinimum = 80
		base.Phases.Quality.E2ERequired = true
		base.Phases.Quality.PRReviewMode = "manual"

	case "regulated":
		base.Phases.Plan.CoverageThreshold = 95
		base.Phases.Plan.Severity.BlockOn = []string{"P0", "P1", "P2"}
		base.Phases.Plan.Severity.AssumeOn = nil
		base.Phases.Plan.Severity.DeferOn = []string{"P3"}
		base.Phases.Code.CoverageMinimum = 90
		base.Phases.Quality.E2ERequired = true
		base.Phases.Quality.PRReviewMode = "manual"

	case "opensource":
		base.Phases.Plan.CoverageThreshold = 75
		base.Phases.Plan.Severity.BlockOn = []string{"P0", "P1"}
		base.Phases.Plan.Severity.AssumeOn = []string{"P2"}
		base.Phases.Plan.Severity.DeferOn = []string{"P3"}
		base.Phases.Code.CoverageMinimum = 70
		base.Phases.Quality.E2ERequired = false
		base.Phases.Quality.PRReviewMode = "auto"

	default:
		// Fall back to startup defaults.
		return DefaultsForType("startup")
	}

	return base
}
