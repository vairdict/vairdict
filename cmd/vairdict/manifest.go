package main

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// ManifestTask is one task declared in a run manifest.
type ManifestTask struct {
	// Name is a user-chosen label unique within the manifest. It is the
	// key used by DependsOn references. Names must match [a-zA-Z0-9_-]+
	// so they're safe in CLI output and logs.
	Name string `yaml:"name"`
	// Intent is the natural-language task description handed to the
	// planner. Required.
	Intent string `yaml:"intent"`
	// Issue optionally links the task to a GitHub issue for PR body +
	// auto-review intent resolution.
	Issue int `yaml:"issue,omitempty"`
	// DependsOn is the list of sibling task names this task waits for.
	DependsOn []string `yaml:"depends_on,omitempty"`
	// Priority is one of "high", "normal", "low" (default: "normal").
	// Drives dispatch order among ready tasks (see internal/deps).
	Priority string `yaml:"priority,omitempty"`
}

// Manifest is the top-level YAML structure accepted by --manifest.
type Manifest struct {
	Tasks []ManifestTask `yaml:"tasks"`
}

var manifestNameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// LoadManifest reads and validates a manifest YAML file. Validation is
// structural only — it checks that names are unique, well-formed, and
// that DependsOn references resolve to names in the same manifest.
// Cycle detection happens later in internal/deps.Graph.Validate once
// the task IDs are generated.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parsing manifest %s: %w", path, err)
	}
	if err := validateManifest(&m); err != nil {
		return nil, fmt.Errorf("validating manifest %s: %w", path, err)
	}
	return &m, nil
}

func validateManifest(m *Manifest) error {
	if len(m.Tasks) == 0 {
		return fmt.Errorf("manifest has no tasks")
	}
	names := make(map[string]bool, len(m.Tasks))
	for i, t := range m.Tasks {
		if t.Name == "" {
			return fmt.Errorf("task %d: name is required", i)
		}
		if !manifestNameRe.MatchString(t.Name) {
			return fmt.Errorf("task %q: name must match [a-zA-Z0-9_-]+", t.Name)
		}
		if names[t.Name] {
			return fmt.Errorf("task %q: duplicate name", t.Name)
		}
		if t.Intent == "" {
			return fmt.Errorf("task %q: intent is required", t.Name)
		}
		names[t.Name] = true
	}
	for _, t := range m.Tasks {
		for _, dep := range t.DependsOn {
			if !names[dep] {
				return fmt.Errorf("task %q: depends_on %q is not a task in this manifest", t.Name, dep)
			}
			if dep == t.Name {
				return fmt.Errorf("task %q: cannot depend on itself", t.Name)
			}
		}
	}
	return nil
}
