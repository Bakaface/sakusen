package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Diagnostic carries a non-fatal validation warning surfaced by ValidateFile
// (e.g. file-based workflow loaded but not referenced from .sortie.yml, which
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

// ValidateFile validates a Sortie project config file (.sortie.yml) without
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

// Diagnose validates a Sortie project config file and returns any non-fatal
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

	// Build the global workflow pool (resolved workflows from ~/.sortie.yml
	// and ~/.sortie/workflows/) so project configs that reference global
	// workflows by string ref validate cleanly. Skipped when the file under
	// validation IS the global config itself.
	globalPool, err := loadGlobalPoolForValidation(path)
	if err != nil {
		return nil, fmt.Errorf("load global config: %w", err)
	}

	return validateProject(&proj, filePool, globalPool)
}

// loadGlobalPoolForValidation resolves the global ~/.sortie.yml into a
// globalWorkflowPool. Returns nil (no global pool) when no global config
// exists or when skipPath matches the global path (avoiding self-recursion
// when the validation target IS the global config).
func loadGlobalPoolForValidation(skipPath string) (*globalWorkflowPool, error) {
	globalYml := getGlobalSortieYmlPath()
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
	if err := loadProjectConfigTier(globalYml, tmpCfg, false); err != nil {
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
// ~/.sortie.yml so that project-level string refs to global workflows
// validate without surfacing "missing file" errors.
func validateProject(proj *ProjectConfig, filePool *workflowFilePool, globalPool *globalWorkflowPool) ([]Diagnostic, error) {
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

	// Capture whether any on-disk files exist before resolution mutates the
	// pool, so we can warn when files exist but .sortie.yml has no listing.
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

	var diagnostics []Diagnostic

	// When on-disk files exist but .sortie.yml has no workflows listing, every
	// file becomes hidden — surface a single aggregate warning rather than one
	// per file (which would all describe the same oversight).
	if hadFiles && len(proj.Workflows) == 0 {
		diagnostics = append(diagnostics, Diagnostic{
			Severity: "warning",
			Message:  "workflows: file(s) under .sortie/workflows/ are hidden because there is no workflows listing in .sortie.yml",
		})
	} else {
		// Otherwise surface a warning for each unreferenced file-based workflow.
		for _, wf := range cfg.Workflows {
			if wf.Hidden {
				diagnostics = append(diagnostics, Diagnostic{
					Severity: "warning",
					Message: fmt.Sprintf(
						"workflow %q from %s is hidden — add it to the .sortie.yml workflows listing to make it active",
						wf.Name, wf.Source),
				})
			}
		}
	}

	return diagnostics, nil
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
