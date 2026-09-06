package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Diagnostic carries a non-fatal validation warning surfaced by ValidateFile
// (e.g. file-based workflow loaded but not referenced from .sakusen.yml, which
// is legal but hides the workflow from menus).
type Diagnostic struct {
	Severity string // "warning"
	Message  string
}

// validOnCompleteValues lists the accepted on_complete values.
var validOnCompleteValues = map[string]bool{
	"":       true, // empty falls back to default
	"commit": true,
	"merge":  true,
	"none":   true,
}

// validTmuxNestedAttachBehaviors lists the accepted tmux_nested_attach_behavior values.
var validTmuxNestedAttachBehaviors = map[string]bool{
	"":       true, // empty falls back to default
	"switch": true,
	"nest":   true,
}

// validPriorities lists the accepted default_priority values.
var validPriorities = map[string]bool{
	"":       true, // empty falls back to default
	"low":    true,
	"medium": true,
	"high":   true,
	"urgent": true,
}

// ValidateFile validates a Sakusen project config file (.sakusen.yml) without
// touching the global merge hierarchy. It catches:
//   - YAML syntax errors
//   - Unknown top-level / nested fields (typos like `worktree_sync_paths`)
//   - Workflow loop misconfiguration (forward gotos, bad max_iterations, etc.)
//   - Invalid summarization strategies
//   - Invalid enum values (git.on_complete, default_priority, tmux_nested_attach_behavior)
//   - Duplicate workflow / step names
//   - File-based workflow errors (missing refs, inline/file collisions, bad filenames)
//
// ValidateFile also surfaces non-fatal warnings (e.g. unreferenced file-based
// workflows) — those are returned via Diagnose for callers that want to display
// them. ValidateFile's bool error return path only fires for fatal issues.
func ValidateFile(path string) error {
	_, err := Diagnose(path)
	return err
}

// Diagnose validates a Sakusen project config file and returns any non-fatal
// warnings collected during validation. Fatal errors are returned via the
// error result.
func Diagnose(path string) ([]Diagnostic, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Surface removed keys (claude:, yolo:, system_prompt:, ...) with their
	// migration messages before the strict decode turns them into generic
	// "field not found" errors.
	if err := checkRemovedProjectKeys(data); err != nil {
		return nil, err
	}

	var proj ProjectConfig
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&proj); err != nil {
		return nil, fmt.Errorf("parse yaml: %w", err)
	}

	baseDir := filepath.Dir(path)
	filePool, err := loadWorkflowFilePool(baseDir)
	if err != nil {
		return nil, err
	}

	// Build the global workflow pool (resolved workflows from ~/.sakusen.yml
	// and ~/.sakusen/workflows/) so project configs that reference global
	// workflows by string ref validate cleanly. Skipped when the file under
	// validation IS the global config itself.
	globalPool, err := loadGlobalPoolForValidation(path)
	if err != nil {
		return nil, fmt.Errorf("load global config: %w", err)
	}

	return validateProject(&proj, filePool, globalPool, baseDir)
}

// loadGlobalPoolForValidation resolves the global ~/.sakusen.yml into a
// globalWorkflowPool. Returns nil (no global pool) when no global config
// exists or when skipPath matches the global path (avoiding self-recursion
// when the validation target IS the global config).
func loadGlobalPoolForValidation(skipPath string) (*globalWorkflowPool, error) {
	globalYml := getGlobalSakusenYmlPath()
	if globalYml == "" {
		return nil, nil
	}
	// Resolve to absolute paths to avoid false negatives when skipPath was
	// passed as a relative path.
	absGlobal, err := filepath.Abs(globalYml)
	if err == nil {
		globalYml = absGlobal
	}
	absSkip, err := filepath.Abs(skipPath)
	if err == nil {
		skipPath = absSkip
	}
	if globalYml == skipPath {
		return nil, nil
	}

	tmpCfg := defaultConfig()
	if err := loadProjectConfig(globalYml, tmpCfg); err != nil {
		// A malformed or unresolvable global config must not break diagnosis of
		// the project file. Fall back to no global pool: project refs to global
		// workflows will then surface as ordinary "unresolved ref" findings
		// rather than a fatal load error unrelated to the file under validation.
		return nil, nil
	}
	return snapshotGlobalPool(tmpCfg), nil
}

// validateProject runs structural validation on a parsed ProjectConfig.
// Returns any non-fatal warnings (e.g. unreferenced file-based workflows).
//
// globalPool, when non-nil, supplies workflows defined in the global
// ~/.sakusen.yml so that project-level string refs to global workflows
// validate without surfacing "missing file" errors.
//
// baseDir is the directory holding the file under validation; it anchors the
// project tier of the {{prompt.<name>}} include search.
func validateProject(proj *ProjectConfig, filePool *workflowFilePool, globalPool *globalWorkflowPool, baseDir string) ([]Diagnostic, error) {
	// Enum sanity
	if !validOnCompleteValues[proj.OnComplete] {
		return nil, fmt.Errorf(`on_complete: invalid value %q (must be "commit", "merge", or "none")`, proj.OnComplete)
	}
	if !validTmuxNestedAttachBehaviors[proj.TmuxNestedAttachBehavior] {
		return nil, fmt.Errorf(`tmux_nested_attach_behavior: invalid value %q (must be "switch" or "nest")`, proj.TmuxNestedAttachBehavior)
	}
	if !validPriorities[proj.DefaultPriority] {
		return nil, fmt.Errorf(`default_priority: invalid value %q (must be "low", "medium", "high", or "urgent")`, proj.DefaultPriority)
	}

	// Agent record shapes are checkable from the file alone. Refs (step/
	// workflow `agent:`, default_agent) are NOT resolved here — they may point
	// at agents defined in the global tier, which single-file diagnosis
	// deliberately doesn't merge; the full Load path validates them.
	if err := validateAgentRecords(proj.Agents); err != nil {
		return nil, err
	}
	// Alias names are checkable from the file alone; targets are not resolved
	// here for the same cross-tier reason as agent refs.
	if err := validateAgentAliasNames(proj.AgentAliases); err != nil {
		return nil, err
	}
	// System-role blocks: only the file-local rules (agent/command exclusivity,
	// timeout syntax). Whether a referenced slug exists and is headless is
	// cross-tier, so it stays in the full load path.
	var mergeConflicts MergeConflictsConfig
	if proj.MergeConflicts != nil {
		mergeConflicts = *proj.MergeConflicts
	}
	var summarizer SummarizerConfig
	if proj.Summarizer != nil {
		summarizer = *proj.Summarizer
	}
	if err := validateRoleBlocks(mergeConflicts, summarizer); err != nil {
		return nil, err
	}

	// Capture whether any on-disk files exist before resolution mutates the
	// pool, so we can warn when files exist but .sakusen.yml has no listing.
	hadFiles := filePool != nil && len(filePool.byName) > 0

	// Reuse the production assembly path to apply identical validation rules
	// (loops, steps, summarization strategies, pins) the daemon enforces at
	// load time.
	cfg := defaultConfig()
	cfg.globalPool = globalPool
	if err := resolveWorkflows(cfg, proj, filePool); err != nil {
		return nil, err
	}

	if err := validateUniqueNames(cfg); err != nil {
		return nil, err
	}

	// Every {{prompt.<name>}} include must resolve to a file on disk — an
	// unresolved include would reach an agent as literal placeholder text.
	// merge_conflicts is carried over by hand: cfg here is a bare
	// defaultConfig() that resolveWorkflows never populates from proj.
	cfg.MergeConflicts = mergeConflicts
	if err := validatePromptIncludes(cfg, PromptDirs(baseDir)); err != nil {
		return nil, err
	}

	var diagnostics []Diagnostic

	// Sub-minute @every cadences parse fine but the scheduler polls on a 30s
	// tick, so they are observed at tick resolution — surface that as a
	// warning rather than silently under-delivering.
	for i := range cfg.Periodic {
		p := &cfg.Periodic[i]
		if rest, ok := strings.CutPrefix(p.Cadence, "@every "); ok {
			if d, err := time.ParseDuration(strings.TrimSpace(rest)); err == nil && d < time.Minute {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: "warning",
					Message: fmt.Sprintf(
						"periodic %q: sub-minute cadence %q is observed at the scheduler's ~30s tick resolution, not to the second",
						p.Name, p.Cadence),
				})
			}
		}
	}

	// When on-disk files exist but .sakusen.yml has no workflows listing, every
	// file becomes hidden — surface a single aggregate warning rather than one
	// per file (which would all describe the same oversight).
	if hadFiles && len(proj.Workflows) == 0 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: "warning",
			Message:  "workflows: file(s) under .sakusen/workflows/ are hidden because there is no workflows listing in .sakusen.yml",
		})
	} else {
		// Otherwise surface a warning for each unreferenced file-based workflow.
		// Inline periodic entries are registered as hidden workflows by design
		// (see buildResolvedConfig) and fire regardless of Hidden, so they are
		// never "unreferenced" — and listing them would be wrong advice.
		for _, wf := range cfg.Workflows {
			if wf.Hidden && wf.Source != "periodic" {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: "warning",
					Message: fmt.Sprintf(
						"workflow %q from %s is hidden — add it to the .sakusen.yml workflows listing to make it active",
						wf.Name, wf.Source),
				})
			}
		}
	}

	return diagnostics, nil
}

// validatePeriodic checks the resolved periodic definitions for correctness:
//  1. cadence parses with the periodic cron parser
//  2. exactly one of workflow / steps is set
//  3. ref-mode: the referenced workflow exists
//  4. when input is empty, no step prompt of the effective workflow may
//     reference {{task.input}} (it would resolve to the empty string)
//  5. names are unique and non-empty
func validatePeriodic(cfg *Config) error {
	seen := make(map[string]bool, len(cfg.Periodic))
	for i := range cfg.Periodic {
		p := &cfg.Periodic[i]
		if p.Name == "" {
			return fmt.Errorf("periodic: entry %d is missing a name", i)
		}
		if seen[p.Name] {
			return fmt.Errorf("periodic: duplicate name %q", p.Name)
		}
		seen[p.Name] = true

		if p.Cadence == "" {
			return fmt.Errorf("periodic %q: cadence is required", p.Name)
		}
		if _, err := ParsePeriodicCadence(p.Cadence); err != nil {
			return fmt.Errorf("periodic %q: invalid cadence %q: %w", p.Name, p.Cadence, err)
		}

		hasWorkflow := p.Workflow != ""
		hasSteps := len(p.Steps) > 0
		switch {
		case hasWorkflow && hasSteps:
			return fmt.Errorf("periodic %q: set exactly one of `workflow` or `steps`, not both", p.Name)
		case !hasWorkflow && !hasSteps:
			return fmt.Errorf("periodic %q: set exactly one of `workflow` or `steps`", p.Name)
		}

		if p.Priority != "" && !validPriorities[p.Priority] {
			return fmt.Errorf("periodic %q: invalid priority %q (must be \"low\", \"medium\", \"high\", or \"urgent\")", p.Name, p.Priority)
		}

		// Resolve the effective steps for the empty-input guard. A ref-mode
		// workflow with its own input pin (WorkflowConfig.Input) satisfies the
		// guard too — createTaskFromRequest falls back to the pin when the
		// request input is empty.
		var steps []StepConfig
		inputPinned := false
		if hasWorkflow {
			wf := findWorkflowByName(cfg, p.Workflow)
			if wf == nil {
				return fmt.Errorf("periodic %q: referenced workflow %q does not exist", p.Name, p.Workflow)
			}
			steps = wf.Steps
			inputPinned = wf.Input != ""
		} else {
			steps = p.Steps
		}

		if p.Input == "" && !inputPinned {
			for _, step := range steps {
				if strings.Contains(step.Prompt, "{{task.input}}") {
					return fmt.Errorf("periodic %q: step %q references {{task.input}} but no `input` is set", p.Name, step.Name)
				}
			}
		}
	}
	return nil
}

// findWorkflowByName locates a workflow by an exact name match in the resolved
// flat engine list (cfg.Workflows — which also carries hidden entries such as
// "periodic:<name>" inline workflows and "<slug>:<name>" track workflows). It
// does NOT add or strip prefixes.
func findWorkflowByName(cfg *Config, name string) *WorkflowConfig {
	for i := range cfg.Workflows {
		if cfg.Workflows[i].Name == name {
			return &cfg.Workflows[i]
		}
	}
	return nil
}

// validateUniqueNames ensures workflow names are unique across the flat list and
// that step names are unique within each workflow.
func validateUniqueNames(cfg *Config) error {
	seenWf := make(map[string]bool, len(cfg.Workflows))
	for _, wf := range cfg.Workflows {
		if seenWf[wf.Name] {
			return fmt.Errorf("workflows: duplicate workflow name %q", wf.Name)
		}
		seenWf[wf.Name] = true
	}

	for _, wf := range cfg.Workflows {
		seen := make(map[string]bool, len(wf.Steps))
		for _, step := range wf.Steps {
			if step.Name == "" {
				return fmt.Errorf("workflow %q: step is missing a name", wf.Name)
			}
			if seen[step.Name] {
				return fmt.Errorf("workflow %q: duplicate step name %q", wf.Name, step.Name)
			}
			seen[step.Name] = true
		}
	}
	return nil
}
