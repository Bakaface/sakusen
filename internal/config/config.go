package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Bakaface/sakusen/internal/tmux"
	"gopkg.in/yaml.v3"
)

// validKebabCaseName matches kebab-case identifiers: workflow filenames
// (extension checked separately), track directory names, and agent slugs.
var validKebabCaseName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func defaultConfig() *Config {
	animEnabled := true
	animDuration := 1500
	return &Config{
		MaxWorkers: 3,
		Git: GitConfig{
			BranchTemplate: "sakusen/{{task_id}}-{{task_slug}}",
		},
		OnComplete: "commit",
		Workflows:  nil, // Empty - DefaultWorkflow() handles fallback
		// Desktop notifications are opt-in: an environment with no sakusen
		// config at all (e.g. the e2e harness, which points HOME/XDG_CONFIG_HOME
		// at a temp dir) must stay silent rather than posting real OS
		// notifications for every agent it completes.
		Notifications: NotificationsConfig{
			Enabled:        false,
			OnComplete:     false,
			OnFailed:       false,
			OnWaitingInput: false,
		},
		Options: OptionsConfig{
			Animation: &AnimationConfig{
				Enabled:  &animEnabled,
				Duration: &animDuration,
			},
		},
		PollInterval: 5 * time.Second,
	}
}

// DefaultWorkflow returns the single-step default workflow when no workflow is configured
func DefaultWorkflow() WorkflowConfig {
	return WorkflowConfig{
		Name: "default",
		Steps: []StepConfig{
			{
				Name:   "implementing",
				Prompt: "Implement the task described in this worktree's CLAUDE.md",
				Mode:   "automatic",
			},
		},
	}
}

// loadCommon loads the global config and global .sakusen.yml into cfg, and
// captures the resolved global workflows into cfg.globalPool so that the
// subsequent project-level load can reference them by name via string refs.
func loadCommon(cfg *Config) error {
	// Load global config (~/.config/sakusen/config.yaml)
	globalPath := getGlobalConfigPath()
	if globalPath != "" {
		if err := loadGlobalConfig(globalPath, cfg); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	// Load global .sakusen.yml (~/.sakusen.yml). cfg.globalPool is still nil at
	// this point, so the global file's own workflows resolve only against its
	// local .sakusen/workflows/ — no self-recursion.
	globalSakusenYml := getGlobalSakusenYmlPath()
	if globalSakusenYml != "" {
		if err := loadProjectConfigTier(globalSakusenYml, cfg, false); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	// Snapshot the global-resolved workflows so the upcoming project load can
	// look them up by name. Done after the global load so it reflects the
	// fully-resolved state (file-based + inline + hidden alike).
	cfg.globalPool = snapshotGlobalPool(cfg)

	return nil
}

// Load loads config from global config, global .sakusen.yml, and project .sakusen.yml,
// returning a merged Config. Loading order (later overrides earlier):
//  1. Built-in defaults
//  2. ~/.config/sakusen/config.yaml (global daemon config)
//  3. ~/.sakusen.yml (global sakusen.yml defaults)
//  4. ./.sakusen.yml (project config)
func Load() (*Config, error) {
	cfg := defaultConfig()
	if err := loadCommon(cfg); err != nil {
		return nil, err
	}

	// Load project config (.sakusen.yml at repo root)
	projectPath := getProjectConfigPath()
	if projectPath != "" {
		if err := loadProjectConfig(projectPath, cfg); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		cfg.ProjectDir = filepath.Dir(projectPath)
		cfg.ProjectConfigFound = true
	}

	cfg.computePaths()

	// Track workflows register AFTER the project-level config resolves (never
	// inside loadProjectConfig, which also runs for the global ~/.sakusen.yml —
	// registering there would double-add global track workflows with the wrong
	// tiering).
	if err := appendTrackWorkflows(cfg, cfg.ProjectDir); err != nil {
		return nil, err
	}

	// The agent registry is finalized (variants expanded, aliases resolved)
	// and refs validated once every tier (global + project + tracks) has
	// merged, since a workflow may reference an agent defined in another tier.
	if err := resolveAndValidateAgents(cfg); err != nil {
		return nil, err
	}

	cfg.Project.AutoDetect = true

	if cfg.ProjectDir != "" {
		cfg.ApplyDetectedProject(cfg.ProjectDir)
	}

	return cfg, nil
}

// LoadForProject loads config for a specific project directory.
func LoadForProject(projectDir string) (*Config, error) {
	cfg := defaultConfig()
	if err := loadCommon(cfg); err != nil {
		return nil, err
	}

	// Load project config (.sakusen.yml)
	projectPath := filepath.Join(projectDir, ".sakusen.yml")
	if _, err := os.Stat(projectPath); err == nil {
		if err := loadProjectConfig(projectPath, cfg); err != nil {
			return nil, err
		}
		cfg.ProjectConfigFound = true
	}

	cfg.ProjectDir = projectDir
	cfg.computePaths()

	// See the matching call in Load() for why this lives here and not inside
	// loadProjectConfig.
	if err := appendTrackWorkflows(cfg, cfg.ProjectDir); err != nil {
		return nil, err
	}

	// See the matching call in Load().
	if err := resolveAndValidateAgents(cfg); err != nil {
		return nil, err
	}

	cfg.Project.AutoDetect = true
	cfg.ApplyDetectedProject(cfg.ProjectDir)

	return cfg, nil
}

func loadGlobalConfig(path string, cfg *Config) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := checkRemovedProjectKeys(data); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	var global GlobalConfig
	if err := yaml.Unmarshal(data, &global); err != nil {
		return err
	}

	overridePositive(&cfg.MaxWorkers, global.MaxWorkers)
	if global.PollInterval != "" {
		if d, err := time.ParseDuration(global.PollInterval); err == nil && d > 0 {
			cfg.PollInterval = d
		} else if err != nil {
			return fmt.Errorf("invalid poll_interval %q: %w", global.PollInterval, err)
		}
	}
	overrideFromPtr(&cfg.Verification, global.Verification)
	cfg.Notifications = global.Notifications
	override(&cfg.TmuxNestedAttachBehavior, global.TmuxNestedAttachBehavior)
	cfg.Agents = mergeAgents(cfg.Agents, global.Agents)
	cfg.AgentAliases = mergeAgentAliases(cfg.AgentAliases, global.AgentAliases)
	override(&cfg.DefaultAgent, global.DefaultAgent)
	overrideFromPtr(&cfg.Summarizer, global.Summarizer)
	if global.Options != nil {
		override(&cfg.Options.Number, global.Options.Number)
		override(&cfg.Options.Branch, global.Options.Branch)
		override(&cfg.Options.Target, global.Options.Target)
		if global.Options.Animation != nil {
			if cfg.Options.Animation == nil {
				cfg.Options.Animation = &AnimationConfig{}
			}
			override(&cfg.Options.Animation.Enabled, global.Options.Animation.Enabled)
			override(&cfg.Options.Animation.Duration, global.Options.Animation.Duration)
		}
	}

	return nil
}

// loadProjectConfig loads a .sakusen.yml at the project tier. Kept as a thin
// wrapper so existing callers (and tests) that always load project-scope
// files keep their signature; loadCommon loads ~/.sakusen.yml with
// projectTier=false via loadProjectConfigTier.
func loadProjectConfig(path string, cfg *Config) error {
	return loadProjectConfigTier(path, cfg, true)
}

// loadProjectConfigTier loads a .sakusen.yml-shaped file into cfg. projectTier
// distinguishes the project ./.sakusen.yml (true) from the global ~/.sakusen.yml
// (false), which shares the same format but is a less-local scope: settings
// explicitly set at the project tier are recorded (OnCompleteFromProject) so
// finalization can apply locality precedence against global workflows.
func loadProjectConfigTier(path string, cfg *Config, projectTier bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	if err := checkRemovedProjectKeys(data); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	var proj ProjectConfig
	if err := yaml.Unmarshal(data, &proj); err != nil {
		return err
	}

	// Discover file-based workflows under <dir>/.sakusen/workflows/ (flat, no subdirs)
	baseDir := filepath.Dir(path)
	filePool, err := loadWorkflowFilePool(baseDir)
	if err != nil {
		return err
	}

	overridePositive(&cfg.MaxWorkers, proj.MaxWorkers)
	if proj.PollInterval != "" {
		d, err := time.ParseDuration(proj.PollInterval)
		if err != nil {
			return fmt.Errorf("invalid poll_interval %q: %w", proj.PollInterval, err)
		}
		if d > 0 {
			cfg.PollInterval = d
		}
	}
	override(&cfg.Git.BaseBranch, proj.Git.BaseBranch)
	override(&cfg.Git.BranchTemplate, proj.Git.BranchTemplate)
	override(&cfg.OnComplete, proj.OnComplete)
	if projectTier && proj.OnComplete != "" {
		cfg.OnCompleteFromProject = true
	}
	override(&cfg.DefaultPriority, proj.DefaultPriority)
	overrideFromPtr(&cfg.Verification, proj.Verification)
	overrideFromPtr(&cfg.Notifications, proj.Notifications)
	override(&cfg.TmuxNestedAttachBehavior, proj.TmuxNestedAttachBehavior)
	overrideIfNotEmpty(&cfg.WorktreeSyncPaths, proj.WorktreeSyncPaths)
	override(&cfg.WorktreeSetupCommand, proj.WorktreeSetupCommand)
	overrideNonEmptySlice(&cfg.WorktreeSetupCommands, proj.WorktreeSetupCommands)
	override(&cfg.TmuxSetupCommand, proj.TmuxSetupCommand)
	cfg.Agents = mergeAgents(cfg.Agents, proj.Agents)
	cfg.AgentAliases = mergeAgentAliases(cfg.AgentAliases, proj.AgentAliases)
	override(&cfg.DefaultAgent, proj.DefaultAgent)
	overrideFromPtr(&cfg.Summarizer, proj.Summarizer)
	if proj.Options != nil {
		override(&cfg.Options.Number, proj.Options.Number)
		override(&cfg.Options.Branch, proj.Options.Branch)
		override(&cfg.Options.Target, proj.Options.Target)
		if proj.Options.Animation != nil {
			if cfg.Options.Animation == nil {
				cfg.Options.Animation = &AnimationConfig{}
			}
			override(&cfg.Options.Animation.Enabled, proj.Options.Animation.Enabled)
			override(&cfg.Options.Animation.Duration, proj.Options.Animation.Duration)
		}
	}

	return resolveWorkflows(cfg, &proj, filePool)
}

// globalWorkflowPool holds workflows resolved from the global ~/.sakusen.yml
// (both inline and file-based under ~/.sakusen/workflows/) keyed by name.
// Project-level string refs that don't match a local .sakusen/workflows/<name>.yml
// fall back to this pool, letting projects reuse globally-defined workflows by
// name.
//
// Distinct from workflowFilePool because there are no "hidden-append"
// semantics here: a global workflow only flows into a project's resolved
// listing when the project explicitly references it.
type globalWorkflowPool struct {
	byName map[string]WorkflowConfig
}

func newGlobalWorkflowPool() *globalWorkflowPool {
	return &globalWorkflowPool{
		byName: make(map[string]WorkflowConfig),
	}
}

// lookup returns the global workflow for name and reports whether it was found.
func (p *globalWorkflowPool) lookup(name string) (WorkflowConfig, bool) {
	if p == nil {
		return WorkflowConfig{}, false
	}
	wf, ok := p.byName[name]
	return wf, ok
}

// add registers a workflow in the pool.
func (p *globalWorkflowPool) add(wf WorkflowConfig) {
	if p == nil {
		return
	}
	p.byName[wf.Name] = wf
}

// snapshotGlobalPool captures the currently-resolved workflows on cfg into a
// pool that project-level config resolution can consult to look up workflows
// by name. Called after the global ~/.sakusen.yml is loaded so that later
// project loads can reference global workflows via string refs.
func snapshotGlobalPool(cfg *Config) *globalWorkflowPool {
	pool := newGlobalWorkflowPool()
	for i := range cfg.Workflows {
		// Everything resolved during the global load is global-scope: both the
		// in-place cfg.Workflows entries (which survive as-is when the project
		// defines no workflows of its own) and the pool copies handed to
		// project-level string refs.
		cfg.Workflows[i].FromGlobal = true
		pool.add(cfg.Workflows[i])
	}
	return pool
}

// workflowFilePool holds workflow definitions discovered on disk under
// .sakusen/workflows/, keyed by name → loaded workflow. Files that haven't been
// referenced from .sakusen.yml at the end of resolution are appended to the
// resolved list as Hidden=true.
type workflowFilePool struct {
	// byName[name] → WorkflowConfig (with Source set, Hidden=false).
	byName map[string]WorkflowConfig
	// order preserves alphabetical iteration order over files for stable
	// Hidden appending.
	order []string
}

func newWorkflowFilePool() *workflowFilePool {
	return &workflowFilePool{
		byName: make(map[string]WorkflowConfig),
	}
}

// lookup returns the file-based workflow for name and reports whether it was found.
func (p *workflowFilePool) lookup(name string) (WorkflowConfig, bool) {
	if p == nil {
		return WorkflowConfig{}, false
	}
	wf, ok := p.byName[name]
	return wf, ok
}

// remove deletes a workflow from the pool (used to mark a file as "claimed"
// by an active string ref so we can identify unreferenced files at the end).
func (p *workflowFilePool) remove(name string) {
	if p == nil {
		return
	}
	delete(p.byName, name)
}

// remainingNames returns the alphabetically-ordered names left in the pool.
// Used to append hidden workflows in stable order.
func (p *workflowFilePool) remainingNames() []string {
	if p == nil {
		return nil
	}
	var names []string
	for _, n := range p.order {
		if _, ok := p.byName[n]; ok {
			names = append(names, n)
		}
	}
	return names
}

// loadWorkflowFilePool scans <baseDir>/.sakusen/workflows/ (flat, no subdirs)
// and returns the discovered workflows. Returns an empty pool when the
// .sakusen/workflows directory doesn't exist (not an error).
func loadWorkflowFilePool(baseDir string) (*workflowFilePool, error) {
	if baseDir == "" {
		return newWorkflowFilePool(), nil
	}
	return loadWorkflowDir(filepath.Join(baseDir, ".sakusen", "workflows"))
}

// loadWorkflowDir scans a workflows directory (flat, no subdirs) and returns
// the discovered workflows. Shared by loadWorkflowFilePool (the classic
// .sakusen/workflows/ tier) and loadTrackWorkflows (per-track
// .sakusen/tracks/<slug>/workflows/ dirs). Returns an empty pool when the
// directory doesn't exist (not an error).
func loadWorkflowDir(root string) (*workflowFilePool, error) {
	pool := newWorkflowFilePool()
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return pool, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return pool, nil
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	// Deterministic order — os.ReadDir already sorts by name, but make it explicit.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("workflows: subdirectories not supported (found %q)", entry.Name())
		}
		fname := entry.Name()
		ext := filepath.Ext(fname)
		if ext != ".yml" && ext != ".yaml" {
			return nil, fmt.Errorf("workflows: invalid file extension %q (must be .yml or .yaml)", fname)
		}
		base := strings.TrimSuffix(fname, ext)
		if !validKebabCaseName.MatchString(base) {
			return nil, fmt.Errorf("workflows: invalid filename %q (must be kebab-case: [a-z0-9-]+)", fname)
		}

		path := filepath.Join(root, fname)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}

		// Reject any `name:` field in file-based workflows — filename is the name.
		if err := assertNoNameField(data); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}

		var wf WorkflowConfig
		if err := yaml.Unmarshal(data, &wf); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		wf.Name = base
		wf.Source = path

		if _, dup := pool.byName[base]; dup {
			return nil, fmt.Errorf("workflows: duplicate file-based workflow %q", base)
		}
		pool.byName[base] = wf
		pool.order = append(pool.order, base)
	}

	return pool, nil
}

// loadTrackWorkflows scans tracksDir (a ".sakusen/tracks" or "~/.sakusen/tracks"
// directory) and returns each track's workflows — discovered under
// <tracksDir>/<slug>/workflows/*.yml — as hidden, namespaced "<slug>:<name>"
// WorkflowConfigs, in slug-alphabetical then name-alphabetical order. Missing
// tracksDir is not an error. Non-directory entries in tracksDir are skipped;
// non-kebab-case slug directory names are a hard error. The per-track
// workflows dirs follow the exact same file rules as .sakusen/workflows/
// (flat, .yml/.yaml only, kebab-case base names, no `name:` field).
func loadTrackWorkflows(tracksDir string) ([]WorkflowConfig, error) {
	info, err := os.Stat(tracksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, nil
	}

	entries, err := os.ReadDir(tracksDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", tracksDir, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var out []WorkflowConfig
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		slug := entry.Name()
		if !validKebabCaseName.MatchString(slug) {
			return nil, fmt.Errorf("tracks: invalid track directory name %q (must be kebab-case)", slug)
		}
		pool, err := loadWorkflowDir(filepath.Join(tracksDir, slug, "workflows"))
		if err != nil {
			return nil, err
		}
		for _, base := range pool.order {
			wf := pool.byName[base]
			wf.Name = slug + ":" + base
			wf.Hidden = true
			out = append(out, wf)
		}
	}
	return out, nil
}

// globalTracksDir returns the global track-workflow root (~/.sakusen/tracks).
// Deliberately NOT derived from getGlobalSakusenYmlPath(): that helper returns
// "" when ~/.sakusen.yml doesn't exist, and global track workflows must resolve
// regardless of whether a global config file is present.
func globalTracksDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".sakusen", "tracks"), nil
}

// appendTrackWorkflows appends project-tier (<projectBaseDir>/.sakusen/tracks)
// then global-tier (~/.sakusen/tracks) track workflows to cfg.Workflows as
// hidden "<slug>:<name>" entries. Project shadows global on identical
// namespaced names (skipped silently, matching the workflow-config precedence
// direction); a project-tier collision with an already-resolved workflow is a
// hard error (practically unreachable — ':' is banned in file-based names).
// Track workflows go through the same step/loop/pin validation as every other
// resolved workflow.
func appendTrackWorkflows(cfg *Config, projectBaseDir string) error {
	seen := make(map[string]bool, len(cfg.Workflows))
	for _, wf := range cfg.Workflows {
		seen[wf.Name] = true
	}

	appendTier := func(tracksDir string, projectTier bool) error {
		wfs, err := loadTrackWorkflows(tracksDir)
		if err != nil {
			return err
		}
		for _, wf := range wfs {
			if seen[wf.Name] {
				if projectTier {
					return fmt.Errorf("tracks: workflow %q collides with an existing workflow", wf.Name)
				}
				continue // project shadows global
			}
			wf.FromGlobal = !projectTier
			if err := wf.ValidatePins(); err != nil {
				return err
			}
			if err := wf.ValidateLoops(); err != nil {
				return fmt.Errorf("workflow %q: %w", wf.Name, err)
			}
			if err := wf.ValidateSteps(); err != nil {
				return fmt.Errorf("workflow %q: %w", wf.Name, err)
			}
			if err := wf.ValidateOnComplete(); err != nil {
				return fmt.Errorf("workflow %q: %w", wf.Name, err)
			}
			cfg.Workflows = append(cfg.Workflows, wf)
			seen[wf.Name] = true
		}
		return nil
	}

	if projectBaseDir != "" {
		if err := appendTier(filepath.Join(projectBaseDir, ".sakusen", "tracks"), true); err != nil {
			return err
		}
	}
	if globalDir, err := globalTracksDir(); err == nil {
		if err := appendTier(globalDir, false); err != nil {
			return err
		}
	}
	return nil
}

// assertNoNameField rejects file-based workflow definitions that set a `name:`
// field. The filename is authoritative; allowing `name:` invites name/file
// drift.
func assertNoNameField(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return nil // surface the parse error from the main decode path
	}
	// The top of a document is a DocumentNode containing one MappingNode.
	root := &node
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Kind == yaml.ScalarNode && key.Value == "name" {
			return fmt.Errorf("file-based workflows must not set `name:` (filename is the name)")
		}
	}
	return nil
}

// resolveWorkflows processes the flat project workflows list into the Config's
// resolved flat list. It merges in file-based workflows from filePool, ensures
// all workflows have names, and validates them.
//
// File-based workflows referenced via string entries in .sakusen.yml become
// active (in listing order). Files in the pool not referenced from .sakusen.yml
// are appended to the resolved list as Hidden=true (alphabetical order for
// stability). Inline + file collision is a hard error.
func resolveWorkflows(cfg *Config, proj *ProjectConfig, filePool *workflowFilePool) error {
	// cfg.globalPool is populated by loadCommon after the global ~/.sakusen.yml
	// is processed. It is nil while the global config itself is being loaded
	// (so global resolution never self-references) and during direct
	// loadProjectConfig() calls from tests that bypass loadCommon.
	globalPool := cfg.globalPool

	hasFilePool := filePool != nil && len(filePool.byName) > 0

	if len(proj.Workflows) > 0 || hasFilePool {
		resolved, err := resolveFlat(proj.Workflows, filePool, globalPool)
		if err != nil {
			return err
		}
		cfg.Workflows = resolved
	}

	// Ensure all workflows have names.
	for i := range cfg.Workflows {
		if cfg.Workflows[i].Name == "" {
			if i == 0 {
				cfg.Workflows[i].Name = "default"
			} else {
				cfg.Workflows[i].Name = fmt.Sprintf("workflow-%d", i+1)
			}
		}
	}

	// Validate workflow configurations (after all workflows are assembled)
	for i := range cfg.Workflows {
		if err := cfg.Workflows[i].ValidatePins(); err != nil {
			return err
		}
		if err := cfg.Workflows[i].ValidateLoops(); err != nil {
			return fmt.Errorf("workflow %q: %w", cfg.Workflows[i].Name, err)
		}
		if err := cfg.Workflows[i].ValidateSteps(); err != nil {
			return fmt.Errorf("workflow %q: %w", cfg.Workflows[i].Name, err)
		}
		if err := cfg.Workflows[i].ValidateOnComplete(); err != nil {
			return fmt.Errorf("workflow %q: %w", cfg.Workflows[i].Name, err)
		}
	}

	return nil
}

// resolveWorkflowSteps expands step *references* (bare-string step entries) in
// wf.Steps into concrete StepConfigs pulled from the same-named base workflow.
//
// The base is the workflow with the same name in globalPool — i.e. the global
// ~/.sakusen.yml workflow this project-level workflow overrides. A project can
// thus reuse a global workflow's steps by name (reordering, dropping, or
// interleaving new inline steps) without re-declaring each one:
//
//	workflows:
//	  - name: the-work        # overrides the global "the-work"
//	    steps:
//	      - planning          # ref → global the-work's "planning" step
//	      - implementing      # ref → global the-work's "implementing" step
//	      - name: reviewing   # new step defined locally
//	        prompt: "..."
//
// Inline step entries pass through unchanged; an inline step whose name matches
// a base step fully overrides it (replace-not-merge, same as workflow-level
// overrides). Referencing a step absent from the base — or having no base
// workflow at all — is a hard error. A no-op when wf has no reference steps, so
// it is safe (and idempotent) to call on any workflow.
func resolveWorkflowSteps(wf *WorkflowConfig, globalPool *globalWorkflowPool) error {
	hasRef := false
	for i := range wf.Steps {
		if wf.Steps[i].ref {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return nil
	}

	base := map[string]StepConfig{}
	if g, ok := globalPool.lookup(wf.Name); ok {
		for _, s := range g.Steps {
			base[s.Name] = s
		}
	}

	out := make([]StepConfig, 0, len(wf.Steps))
	seen := make(map[string]bool, len(wf.Steps))
	for _, s := range wf.Steps {
		if s.ref {
			def, ok := base[s.Name]
			if !ok {
				return fmt.Errorf("workflows: workflow %q references step %q, which is not defined in the base workflow (define it inline or add it to the same-named global workflow)", wf.Name, s.Name)
			}
			s = def
		}
		if s.Name != "" {
			if seen[s.Name] {
				return fmt.Errorf("workflows: workflow %q has a duplicate step %q", wf.Name, s.Name)
			}
			seen[s.Name] = true
		}
		out = append(out, s)
	}
	wf.Steps = out
	return nil
}

// resolveFlat expands the flat workflows entries (string refs + inline defs)
// into a flat slice of WorkflowConfig. Active workflows come first in listing
// order; any files in the local pool not referenced are appended as Hidden.
//
// String refs are resolved against the local file pool first; if not found,
// globalPool (workflows defined in the global ~/.sakusen.yml, both inline and
// file-based) is consulted as a fallback. This lets project configs reuse
// globally-defined workflows by name.
//
// Project-level inline definitions or project-level local files with the same
// name as a global workflow are allowed and override the global — only
// inline-vs-file collisions WITHIN the project's own scope are an error.
func resolveFlat(entries []WorkflowEntry, filePool *workflowFilePool, globalPool *globalWorkflowPool) ([]WorkflowConfig, error) {
	// Track names seen so we can flag duplicates and inline/file collisions.
	seen := make(map[string]bool, len(entries))
	out := make([]WorkflowConfig, 0, len(entries))

	for _, entry := range entries {
		switch {
		case entry.Ref != "":
			name := entry.Ref
			if seen[name] {
				return nil, fmt.Errorf("workflows: duplicate workflow name %q", name)
			}
			// Local file pool wins over the global pool when both define the
			// same name (project-level overrides global-level).
			wf, ok := filePool.lookup(name)
			if ok {
				filePool.remove(name)
			} else if globalWf, gok := globalPool.lookup(name); gok {
				wf = globalWf
				ok = true
			}
			if !ok {
				return nil, fmt.Errorf("workflows: referenced workflow %q has no file at .sakusen/workflows/%s.yml and is not defined in the global config", name, name)
			}
			wf.Hidden = false
			if err := resolveWorkflowSteps(&wf, globalPool); err != nil {
				return nil, err
			}
			out = append(out, wf)
			seen[name] = true
		case entry.Inline != nil:
			wf := *entry.Inline
			if wf.Name == "" {
				return nil, fmt.Errorf("workflows: inline workflow is missing a name")
			}
			if seen[wf.Name] {
				return nil, fmt.Errorf("workflows: duplicate workflow name %q", wf.Name)
			}
			// Inline-vs-file collision is only an error within the project's
			// own scope. An inline definition that shadows a global workflow
			// is a legal override.
			if _, dup := filePool.lookup(wf.Name); dup {
				return nil, fmt.Errorf("workflows: inline workflow %q collides with file at .sakusen/workflows/%s.yml — define it in one place only", wf.Name, wf.Name)
			}
			wf.Source = "inline"
			wf.Hidden = false
			if err := resolveWorkflowSteps(&wf, globalPool); err != nil {
				return nil, err
			}
			out = append(out, wf)
			seen[wf.Name] = true
		default:
			return nil, fmt.Errorf("workflows: empty entry")
		}
	}

	// Append unreferenced file-based workflows as hidden, alphabetical order.
	for _, name := range filePool.remainingNames() {
		wf, ok := filePool.lookup(name)
		if !ok {
			continue
		}
		wf.Hidden = true
		if err := resolveWorkflowSteps(&wf, globalPool); err != nil {
			return nil, err
		}
		out = append(out, wf)
	}

	return out, nil
}

func (c *Config) computePaths() {
	if c.ProjectDir == "" {
		cwd, _ := os.Getwd()
		c.ProjectDir = cwd
	}

	// Daemon paths are global (under ~/.config/sakusen/)
	globalDir := getGlobalDataDir()
	c.DatabasePath = filepath.Join(globalDir, "tasks.db")
	c.SocketPath = filepath.Join(globalDir, "daemon.sock")
	c.PidFile = filepath.Join(globalDir, "daemon.pid")
}

// getGlobalDataDir returns the global data directory for daemon state.
func getGlobalDataDir() string {
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "sakusen")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "sakusen")
	}
	return filepath.Join(homeDir, ".config", "sakusen")
}

// GetGlobalDataDir is the exported version for use by other packages.
func GetGlobalDataDir() string {
	return getGlobalDataDir()
}

// EnsureDirs creates the .sakusen directory and any parent dirs needed.
func (c *Config) EnsureDirs() error {
	dirs := []string{
		filepath.Dir(c.SocketPath),
		filepath.Dir(c.PidFile),
		filepath.Dir(c.DatabasePath),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return err
		}
	}

	return nil
}

// GetDatabasePath returns the global database path.
// The projectDir parameter is kept for backward compatibility but is no longer used.
func (c *Config) GetDatabasePath(_ string) string {
	return c.DatabasePath
}

// ApplyDetectedProject applies auto-detected project settings.
func (c *Config) ApplyDetectedProject(dir string) {
	if !c.Project.AutoDetect {
		return
	}

	detected := DetectProject(dir)

	if c.Project.Name == "" {
		c.Project.Name = ProjectNameFromPath(dir)
	}

	if detected.Type == ProjectTypeUnknown {
		return
	}
	if c.Project.Commands.Test == "" {
		c.Project.Commands.Test = detected.Commands.Test
	}
	if c.Project.Commands.Lint == "" {
		c.Project.Commands.Lint = detected.Commands.Lint
	}
	if c.Project.Commands.Build == "" {
		c.Project.Commands.Build = detected.Commands.Build
	}
}

func getGlobalConfigPath() string {
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "sakusen", "config.yaml")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	return filepath.Join(homeDir, ".config", "sakusen", "config.yaml")
}

func getGlobalSakusenYmlPath() string {
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		path := filepath.Join(xdgConfig, "sakusen", "config.yml")
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(homeDir, ".sakusen.yml")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

func getProjectConfigPath() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	// Look for .sakusen.yml at repo root
	path := filepath.Join(cwd, ".sakusen.yml")
	if _, err := os.Stat(path); err == nil {
		return path
	}

	return ""
}

// SanitizeProjectName replaces characters that are problematic for downstream
// consumers (e.g. tmux silently converts dots to underscores, breaking session
// prefix matching). Applied at project name creation time so all consumers get
// a clean, consistent name.
//
// This delegates to tmux.SanitizeName, the single source of truth for the
// sanitization rule, since the reason it exists here is exactly "make a name
// tmux will accept".
func SanitizeProjectName(name string) string {
	return tmux.SanitizeName(name)
}

// ProjectNameFromPath derives the canonical project name from a directory path.
// This is the single source of truth for converting a filesystem path into the
// name used as a database key. All call sites that need to look up or store a
// project by its directory must route through this helper to avoid sanitization
// drift between write and read paths (e.g. ".pai" → stored as "_pai").
func ProjectNameFromPath(path string) string {
	return SanitizeProjectName(filepath.Base(path))
}
